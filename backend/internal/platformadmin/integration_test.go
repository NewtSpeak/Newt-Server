package platformadmin_test

// 集成测试：需要真实 PostgreSQL（运行方式见 clientapi/integration_test.go 头注释）：
//
//	TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/owl_test?sslmode=disable' go test ./internal/platformadmin/
//
// 覆盖重点（系统管理员修改自己的用户名）：
//  1. 改名成功返回新用户名，并收到定向 USER_UPDATE 事件；
//  2. 改名后旧 refresh token 仍可续期（不吊销会话语义）；
//  3. 当前密码错误 403 INVALID_PASSWORD；
//  4. 非法用户名（长度/空白/@）400 INVALID_USERNAME；
//  5. 撞名与 LOWER 大小写变体撞名 409 USERNAME_TAKEN；
//  6. 自身大小写变体重命名放行。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/httpapi"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/platformadmin"
	"github.com/owlspeak/owl-server/backend/internal/security"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const testSecret = "platformadmin-integration-secret"

type eventCollector struct {
	mu     sync.Mutex
	events []eventbus.Event
}

func (ec *eventCollector) handle(event eventbus.Event) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.events = append(ec.events, event)
}

func (ec *eventCollector) wait(t *testing.T, description string, match func(eventbus.Event) bool) eventbus.Event {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ec.mu.Lock()
		for _, event := range ec.events {
			if match(event) {
				ec.mu.Unlock()
				return event
			}
		}
		ec.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待事件超时: %s", description)
	return eventbus.Event{}
}

type testEnv struct {
	router *gin.Engine
	db     *gorm.DB
	events *eventCollector
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过数据库集成测试")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := db.AutoMigrate(model.Models()...); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	gin.SetMode(gin.TestMode)
	bus := eventbus.New()
	collector := &eventCollector{}
	bus.Subscribe(collector.handle)

	tokens := security.NewTokenManager(testSecret, time.Minute)
	router := gin.New()
	api := httpapi.New(db, tokens, time.Hour)
	api.AttachEventBus(bus)
	v1 := router.Group("/api/v1")
	api.RegisterRoutes(v1)
	deps := appdeps.Deps{DB: db, Bus: bus, Auth: api.AuthMiddleware(), CurrentUser: httpapi.CurrentUser}
	if err := platformadmin.Register(v1, deps); err != nil {
		t.Fatalf("挂载 platformadmin 失败: %v", err)
	}
	return &testEnv{router: router, db: db, events: collector}
}

func (env *testEnv) request(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, req)
	return recorder
}

func decode[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var result T
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v (%s)", err, recorder.Body.String())
	}
	return result
}

type tokenPair struct {
	AccessToken  string     `json:"access_token"`
	RefreshToken string     `json:"refresh_token"`
	User         model.User `json:"user"`
}

type errorBody struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

// createUser 直接落库创建用户（绕过首用户注册闸门），返回明文密码。
func (env *testEnv) createUser(t *testing.T, username string, admin bool) (model.User, string) {
	t.Helper()
	password := "password-123"
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatalf("哈希密码失败: %v", err)
	}
	user := model.User{
		ID: uuid.New(), Username: username,
		Email:        fmt.Sprintf("%s-%d@test.local", username, time.Now().UnixNano()),
		PasswordHash: hash, SystemAdmin: admin,
	}
	if err := env.db.Create(&user).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	return user, password
}

// loginAdmin 后台登录取 admin 受众 token。
func (env *testEnv) loginAdmin(t *testing.T, identifier, password string) tokenPair {
	t.Helper()
	recorder := env.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"identifier": identifier, "password": password,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("后台登录失败: %d %s", recorder.Code, recorder.Body.String())
	}
	return decode[tokenPair](t, recorder)
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano()%1e12)
}

func TestChangeOwnUsername(t *testing.T) {
	env := newEnv(t)
	admin, password := env.createUser(t, uniqueName("boss"), true)
	session := env.loginAdmin(t, admin.Username, password)

	t.Run("改名成功且不吊销会话", func(t *testing.T) {
		next := uniqueName("chief")
		recorder := env.request(t, http.MethodPatch, "/api/v1/admin/account/username", session.AccessToken,
			map[string]string{"username": next, "password": password})
		if recorder.Code != http.StatusOK {
			t.Fatalf("改名失败: %d %s", recorder.Code, recorder.Body.String())
		}
		updated := decode[model.User](t, recorder)
		if updated.Username != next {
			t.Fatalf("期望用户名 %s，实际 %s", next, updated.Username)
		}
		env.events.wait(t, "定向 USER_UPDATE", func(event eventbus.Event) bool {
			if event.Type != eventbus.EventUserUpdate {
				return false
			}
			for _, id := range event.UserIDs {
				if id == admin.ID {
					return true
				}
			}
			return false
		})
		// 旧 refresh token 仍可续期：改名不吊销会话。
		refresh := env.request(t, http.MethodPost, "/api/v1/auth/refresh", "",
			map[string]string{"refresh_token": session.RefreshToken})
		if refresh.Code != http.StatusOK {
			t.Fatalf("改名后旧 refresh token 应仍可续期: %d %s", refresh.Code, refresh.Body.String())
		}
		session = decode[tokenPair](t, refresh)
		admin.Username = next
	})

	t.Run("当前密码错误", func(t *testing.T) {
		recorder := env.request(t, http.MethodPatch, "/api/v1/admin/account/username", session.AccessToken,
			map[string]string{"username": uniqueName("x"), "password": "wrong-password"})
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("期望 403，实际 %d %s", recorder.Code, recorder.Body.String())
		}
		if body := decode[errorBody](t, recorder); body.Error.Code != "INVALID_PASSWORD" {
			t.Fatalf("期望 INVALID_PASSWORD，实际 %s", body.Error.Code)
		}
	})

	t.Run("非法用户名", func(t *testing.T) {
		for _, bad := range []string{"a", "has space", "who@where"} {
			recorder := env.request(t, http.MethodPatch, "/api/v1/admin/account/username", session.AccessToken,
				map[string]string{"username": bad, "password": password})
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("用户名 %q 期望 400，实际 %d %s", bad, recorder.Code, recorder.Body.String())
			}
			if body := decode[errorBody](t, recorder); body.Error.Code != "INVALID_USERNAME" {
				t.Fatalf("用户名 %q 期望 INVALID_USERNAME，实际 %s", bad, body.Error.Code)
			}
		}
	})

	t.Run("撞名与大小写变体撞名", func(t *testing.T) {
		other, _ := env.createUser(t, uniqueName("taken"), false)
		for _, clash := range []string{other.Username, "TAKEN" + other.Username[5:]} {
			recorder := env.request(t, http.MethodPatch, "/api/v1/admin/account/username", session.AccessToken,
				map[string]string{"username": clash, "password": password})
			if recorder.Code != http.StatusConflict {
				t.Fatalf("用户名 %q 期望 409，实际 %d %s", clash, recorder.Code, recorder.Body.String())
			}
			if body := decode[errorBody](t, recorder); body.Error.Code != "USERNAME_TAKEN" {
				t.Fatalf("期望 USERNAME_TAKEN，实际 %s", body.Error.Code)
			}
		}
	})

	t.Run("自身大小写变体重命名放行", func(t *testing.T) {
		variant := "C" + admin.Username[1:]
		recorder := env.request(t, http.MethodPatch, "/api/v1/admin/account/username", session.AccessToken,
			map[string]string{"username": variant, "password": password})
		if recorder.Code != http.StatusOK {
			t.Fatalf("自身大小写重命名应放行: %d %s", recorder.Code, recorder.Body.String())
		}
		if updated := decode[model.User](t, recorder); updated.Username != variant {
			t.Fatalf("期望用户名 %s，实际 %s", variant, updated.Username)
		}
	})
}
