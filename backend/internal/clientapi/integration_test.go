package clientapi_test

// 集成测试：需要真实 PostgreSQL（gorm 无内存 sqlite 驱动，纯 mock 难以覆盖
// 注册/登录/refresh 轮换等依赖数据库事务与唯一索引的路径）。
//
// 运行方式（默认跳过，不影响 go test ./...）：
//
//	TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/owl_test?sslmode=disable' go test ./internal/clientapi/
//
// 测试自带随机后缀，可对同一数据库重复运行；会执行 AutoMigrate（勿指向生产库）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/clientapi"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/httpapi"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/security"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const integrationSecret = "integration-secret-integration-32"

// newIntegrationRouter 同时挂后台（/api/v1）与用户端（/gapi/v1），复现生产装配的隔离拓扑。
func newIntegrationRouter(t *testing.T) (*gin.Engine, *gorm.DB, *security.TokenManager) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过数据库集成测试（说明见文件头注释）")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := db.AutoMigrate(model.Models()...); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	gin.SetMode(gin.TestMode)
	cfg := config.Config{JWTSecret: integrationSecret, AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour}
	tokens := security.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL)
	router := gin.New()
	api := httpapi.New(db, tokens, cfg.RefreshTokenTTL)
	api.RegisterRoutes(router.Group("/api/v1"))
	deps := appdeps.Deps{DB: db, Bus: eventbus.New(), Cfg: cfg, Auth: api.AuthMiddleware(), CurrentUser: httpapi.CurrentUser}
	if err := clientapi.Register(router.Group("/gapi/v1"), deps); err != nil {
		t.Fatalf("挂载用户端 API 失败: %v", err)
	}
	return router, db, tokens
}

func doJSON(t *testing.T, router *gin.Engine, method, path, token string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求失败: %v", err)
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
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	parsed := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec, parsed
}

func mustUUID(t *testing.T, raw string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(raw)
	if err != nil {
		t.Fatalf("解析 UUID 失败: %v", err)
	}
	return id
}

func mustNewUUID(t *testing.T) uuid.UUID {
	t.Helper()
	return uuid.New()
}

// randomAccount 生成随机账号资料，保证对同一测试库可重复运行。
func randomAccount() (username, email string) {
	suffix := fmt.Sprintf("%08x", rand.Uint32())
	return "it_" + suffix, "it_" + suffix + "@test.local"
}

func signup(t *testing.T, router *gin.Engine, username, email string) map[string]any {
	t.Helper()
	rec, body := doJSON(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", map[string]string{
		"username": username, "email": email, "password": "password123",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("开放注册返回 %d: %s", rec.Code, rec.Body.String())
	}
	return body
}

// TestOpenSignupAndDuplicate 开放注册成功、重复注册（用户名或邮箱占用）返回 409。
func TestOpenSignupAndDuplicate(t *testing.T) {
	router, _, _ := newIntegrationRouter(t)
	username, email := randomAccount()
	body := signup(t, router, username, email)
	user := body["user"].(map[string]any)
	if user["system_admin"] != false {
		t.Fatalf("开放注册的用户不应是系统管理员: %v", user)
	}
	if body["access_token"] == "" || body["refresh_token"] == "" {
		t.Fatalf("注册未返回令牌对: %v", body)
	}

	rec, _ := doJSON(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", map[string]string{
		"username": username, "email": email, "password": "password123",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("重复注册返回 %d，期待 409: %s", rec.Code, rec.Body.String())
	}
}

// TestCrossAudienceRejection client token 打 /api/v1 → 401；admin token 打 /gapi/v1 → 401。
func TestCrossAudienceRejection(t *testing.T) {
	router, _, tokens := newIntegrationRouter(t)
	username, email := randomAccount()
	body := signup(t, router, username, email)
	clientToken := body["access_token"].(string)

	// client token 可正常访问用户端。
	rec, _ := doJSON(t, router, http.MethodGet, "/gapi/v1/users/@me", clientToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("client token 访问 /gapi/v1/users/@me 返回 %d", rec.Code)
	}
	// client token 打后台一律 401。
	rec, _ = doJSON(t, router, http.MethodGet, "/api/v1/auth/me", clientToken, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("client token 打后台 API 返回 %d，期待 401", rec.Code)
	}
	rec, _ = doJSON(t, router, http.MethodGet, "/api/v1/guilds", clientToken, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("client token 打后台 /guilds 返回 %d，期待 401", rec.Code)
	}

	// admin token（直接签发，无需真实后台账号）打用户端一律 401。
	user := body["user"].(map[string]any)
	adminToken, _, err := tokens.AccessToken(mustUUID(t, user["id"].(string)))
	if err != nil {
		t.Fatalf("签发 admin token 失败: %v", err)
	}
	rec, _ = doJSON(t, router, http.MethodGet, "/gapi/v1/users/@me", adminToken, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("admin token 打用户端 API 返回 %d，期待 401", rec.Code)
	}
}

// TestNonAdminBackendLoginRejected 非系统管理员在后台登录被拒（统一「账号或密码错误」），
// 同一账号在用户端可正常登录。
func TestNonAdminBackendLoginRejected(t *testing.T) {
	router, _, _ := newIntegrationRouter(t)
	username, email := randomAccount()
	signup(t, router, username, email)

	rec, body := doJSON(t, router, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"identifier": email, "password": "password123",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("非系统管登后台返回 %d，期待 401: %s", rec.Code, rec.Body.String())
	}
	// 不暴露存在性：错误信息与密码错误完全一致。
	if msg := body["error"].(map[string]any)["message"]; msg != "账号或密码错误" {
		t.Fatalf("错误信息泄露账号存在性: %v", msg)
	}

	rec, _ = doJSON(t, router, http.MethodPost, "/gapi/v1/auth/login", "", map[string]string{
		"identifier": email, "password": "password123",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("同一账号在用户端登录返回 %d，期待 200: %s", rec.Code, rec.Body.String())
	}
}

// TestClientRefreshRotation 用户端 refresh 轮换：旧令牌一次性使用，且不可换取后台凭证。
func TestClientRefreshRotation(t *testing.T) {
	router, _, _ := newIntegrationRouter(t)
	username, email := randomAccount()
	body := signup(t, router, username, email)
	firstRefresh := body["refresh_token"].(string)

	// 用户端 refresh token 不可在后台换取凭证。
	rec, _ := doJSON(t, router, http.MethodPost, "/api/v1/auth/refresh", "", map[string]string{"refresh_token": firstRefresh})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("client refresh token 打后台 refresh 返回 %d，期待 401", rec.Code)
	}

	// 正常轮换：拿到新令牌对。
	rec, rotated := doJSON(t, router, http.MethodPost, "/gapi/v1/auth/refresh", "", map[string]string{"refresh_token": firstRefresh})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh 轮换返回 %d: %s", rec.Code, rec.Body.String())
	}
	secondRefresh := rotated["refresh_token"].(string)
	if secondRefresh == firstRefresh {
		t.Fatalf("轮换后 refresh token 未更新")
	}

	// 旧令牌已吊销，重放返回 401。
	rec, _ = doJSON(t, router, http.MethodPost, "/gapi/v1/auth/refresh", "", map[string]string{"refresh_token": firstRefresh})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("重放旧 refresh token 返回 %d，期待 401", rec.Code)
	}

	// 新令牌可继续使用；logout 后失效。
	rec, _ = doJSON(t, router, http.MethodPost, "/gapi/v1/auth/logout", "", map[string]string{"refresh_token": secondRefresh})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout 返回 %d，期待 204", rec.Code)
	}
	rec, _ = doJSON(t, router, http.MethodPost, "/gapi/v1/auth/refresh", "", map[string]string{"refresh_token": secondRefresh})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("logout 后 refresh token 仍可用（返回 %d），期待 401", rec.Code)
	}
}

// TestClientGuildFlow 用户端资源端点冒烟：建服 → 我加入的服务器 → 频道/成员 → 邀请加入。
func TestClientGuildFlow(t *testing.T) {
	router, db, _ := newIntegrationRouter(t)
	username, email := randomAccount()
	owner := signup(t, router, username, email)
	ownerToken := owner["access_token"].(string)

	// 建服。
	rec, guild := doJSON(t, router, http.MethodPost, "/gapi/v1/guilds", ownerToken, map[string]string{"name": "集成测试服 " + username})
	if rec.Code != http.StatusCreated {
		t.Fatalf("用户端建服返回 %d: %s", rec.Code, rec.Body.String())
	}
	guildID := guild["id"].(string)

	// 我加入的服务器应包含新服。
	rec, _ = doJSON(t, router, http.MethodGet, "/gapi/v1/users/@me/guilds", ownerToken, nil)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(guildID)) {
		t.Fatalf("我加入的服务器缺少新建服（%d）: %s", rec.Code, rec.Body.String())
	}

	// 频道与成员列表可访问。
	for _, path := range []string{"/channels", "/members"} {
		rec, _ = doJSON(t, router, http.MethodGet, "/gapi/v1/guilds/"+guildID+"/"+path[1:], ownerToken, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s 返回 %d: %s", path, rec.Code, rec.Body.String())
		}
	}

	// 非成员访问返回 404（防扫频语义）。
	strangerName, strangerEmail := randomAccount()
	stranger := signup(t, router, strangerName, strangerEmail)
	strangerToken := stranger["access_token"].(string)
	rec, _ = doJSON(t, router, http.MethodGet, "/gapi/v1/guilds/"+guildID+"/channels", strangerToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非成员访问频道列表返回 %d，期待 404", rec.Code)
	}

	// 直接落库造一张邀请（用户端暂无创建邀请端点），验证凭码加入。
	invite := model.Invite{ID: mustNewUUID(t), GuildID: mustUUID(t, guildID), Code: "it" + fmt.Sprintf("%08x", rand.Uint32()), CreatedBy: mustUUID(t, owner["user"].(map[string]any)["id"].(string))}
	if err := db.Create(&invite).Error; err != nil {
		t.Fatalf("插入测试邀请失败: %v", err)
	}
	rec, _ = doJSON(t, router, http.MethodPost, "/gapi/v1/invites/"+invite.Code+"/join", strangerToken, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("凭邀请码加入返回 %d: %s", rec.Code, rec.Body.String())
	}
	// 加入后可见频道列表。
	rec, _ = doJSON(t, router, http.MethodGet, "/gapi/v1/guilds/"+guildID+"/channels", strangerToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("加入后访问频道列表返回 %d", rec.Code)
	}

	// 被 Ban 用户拒绝加入。
	bannedName, bannedEmail := randomAccount()
	banned := signup(t, router, bannedName, bannedEmail)
	bannedID := mustUUID(t, banned["user"].(map[string]any)["id"].(string))
	ban := model.GuildBan{ID: mustNewUUID(t), GuildID: mustUUID(t, guildID), UserID: bannedID, CreatedBy: invite.CreatedBy}
	if err := db.Create(&ban).Error; err != nil {
		t.Fatalf("插入测试 Ban 失败: %v", err)
	}
	rec, _ = doJSON(t, router, http.MethodPost, "/gapi/v1/invites/"+invite.Code+"/join", banned["access_token"].(string), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("被 Ban 用户加入返回 %d，期待 403", rec.Code)
	}
}
