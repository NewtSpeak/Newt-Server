package userapi_test

// 集成测试：需要真实 PostgreSQL（运行方式见 clientapi/integration_test.go 头注释）：
//
//	TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/owl_test?sslmode=disable' go test ./internal/userapi/
//
// 覆盖重点（任务验收项）：
//  1. 资料 CRUD（display_name/bio 校验、头像上传/删除）与 USER_UPDATE 事件；
//  2. 改密码后除当前会话外其余 refresh token 全部失效；
//  3. 会话列表/吊销（当前会话标记、跨轮换的会话链）；
//  4. GET /users/:id 共同 guild 可见性（无共同服 404、不泄露私有字段）；
//  5. settings 按 top-level key 合并、大小/JSON 校验与 USER_SETTINGS_UPDATE 事件；
//  6. 双平面：后台（/api/v1）同样挂载。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/clientapi"
	"github.com/newtspeak/newt-server/backend/internal/config"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/httpapi"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/security"
	"github.com/newtspeak/newt-server/backend/internal/userapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const testSecret = "userapi-integration-secret-32chr"

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
	router  *gin.Engine
	db      *gorm.DB
	events  *eventCollector
	dataDir string
}

// newEnv 复现生产装配：后台 /api/v1（httpapi + userapi）+ 用户端 /gapi/v1（clientapi 全量）。
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
	dataDir := t.TempDir()
	cfg := config.Config{JWTSecret: testSecret, AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour, DataDir: dataDir}
	bus := eventbus.New()
	collector := &eventCollector{}
	bus.Subscribe(collector.handle)

	tokens := security.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL)
	router := gin.New()
	api := httpapi.New(db, tokens, cfg.RefreshTokenTTL)
	api.AttachEventBus(bus)
	v1 := router.Group("/api/v1")
	api.RegisterRoutes(v1)
	deps := appdeps.Deps{DB: db, Bus: bus, Cfg: cfg, Auth: api.AuthMiddleware(), CurrentUser: httpapi.CurrentUser}
	if err := userapi.Register(v1, deps); err != nil {
		t.Fatalf("挂载后台 userapi 失败: %v", err)
	}
	if err := clientapi.Register(router.Group("/gapi/v1"), deps); err != nil {
		t.Fatalf("挂载用户端 API 失败: %v", err)
	}
	return &testEnv{router: router, db: db, events: collector, dataDir: dataDir}
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

// signup 用户端注册（每次生成唯一用户名）。
func (env *testEnv) signup(t *testing.T) tokenPair {
	t.Helper()
	name := fmt.Sprintf("u%d%04d", time.Now().UnixNano()%1e10, rand.Intn(10000))
	recorder := env.request(t, http.MethodPost, "/gapi/v1/auth/signup", "", map[string]string{
		"username": name, "email": name + "@test.local", "password": "password-123",
	})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("注册失败: %d %s", recorder.Code, recorder.Body.String())
	}
	return decode[tokenPair](t, recorder)
}

// login 用同一账号再开一个会话。
func (env *testEnv) login(t *testing.T, username, password string) tokenPair {
	t.Helper()
	recorder := env.request(t, http.MethodPost, "/gapi/v1/auth/login", "", map[string]string{
		"identifier": username, "password": password,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("登录失败: %d %s", recorder.Code, recorder.Body.String())
	}
	return decode[tokenPair](t, recorder)
}

// joinGuild 直接落库建 guild 与成员关系（结构管理非本测试重点）。
func (env *testEnv) joinGuild(t *testing.T, guildID uuid.UUID, userIDs ...uuid.UUID) {
	t.Helper()
	guild := model.Guild{ID: guildID, Name: "g-" + guildID.String()[:8], OwnerUserID: userIDs[0]}
	if err := env.db.Create(&guild).Error; err != nil {
		t.Fatalf("建服失败: %v", err)
	}
	for _, userID := range userIDs {
		member := model.Member{ID: uuid.New(), GuildID: guildID, UserID: userID}
		if err := env.db.Create(&member).Error; err != nil {
			t.Fatalf("加成员失败: %v", err)
		}
	}
}

// ---- 资料 CRUD 与 USER_UPDATE 事件 ----

func TestProfilePatchAndUserUpdateEvent(t *testing.T) {
	env := newEnv(t)
	alice := env.signup(t)
	bob := env.signup(t)
	guildID := uuid.New()
	env.joinGuild(t, guildID, alice.User.ID, bob.User.ID)

	// 校验失败：显示名超长 / bio 超长。
	if r := env.request(t, http.MethodPatch, "/gapi/v1/users/@me", alice.AccessToken,
		map[string]string{"display_name": strings.Repeat("名", 33)}); r.Code != http.StatusBadRequest {
		t.Fatalf("超长显示名应 400，实际 %d", r.Code)
	}
	if r := env.request(t, http.MethodPatch, "/gapi/v1/users/@me", alice.AccessToken,
		map[string]string{"bio": strings.Repeat("签", 191)}); r.Code != http.StatusBadRequest {
		t.Fatalf("超长 bio 应 400，实际 %d", r.Code)
	}

	recorder := env.request(t, http.MethodPatch, "/gapi/v1/users/@me", alice.AccessToken,
		map[string]string{"display_name": "小A", "bio": "你好世界"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("PATCH 资料失败: %d %s", recorder.Code, recorder.Body.String())
	}
	updated := decode[model.User](t, recorder)
	if updated.DisplayName != "小A" || updated.Bio != "你好世界" {
		t.Fatalf("资料未生效: %+v", updated)
	}

	// GET /users/@me 反映变更。
	me := decode[model.User](t, env.request(t, http.MethodGet, "/gapi/v1/users/@me", alice.AccessToken, nil))
	if me.DisplayName != "小A" || me.Bio != "你好世界" {
		t.Fatalf("GET @me 未反映变更: %+v", me)
	}

	// USER_UPDATE：guild 广播 + 本人定向，载荷为公开投影。
	env.events.wait(t, "USER_UPDATE guild 广播", func(e eventbus.Event) bool {
		if e.Type != eventbus.EventUserUpdate || e.GuildID == nil || *e.GuildID != guildID {
			return false
		}
		p, ok := e.Payload.(eventbus.UserUpdatePayload)
		return ok && p.ID == alice.User.ID && p.DisplayName == "小A" && p.Bio == "你好世界"
	})
	env.events.wait(t, "USER_UPDATE 本人定向", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventUserUpdate && len(e.UserIDs) == 1 && e.UserIDs[0] == alice.User.ID
	})
}

// TestProfileOnAdminPlane 后台平面（/api/v1）同样挂载 GET/PATCH /users/@me。
func TestProfileOnAdminPlane(t *testing.T) {
	env := newEnv(t)
	// 后台初始化注册仅允许首个用户；测试库已有数据时改走 login。
	name := fmt.Sprintf("adm%d%04d", time.Now().UnixNano()%1e10, rand.Intn(10000))
	password := "password-123"
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	admin := model.User{ID: uuid.New(), Username: name, Email: name + "@test.local", PasswordHash: hash, SystemAdmin: true}
	if err := env.db.Create(&admin).Error; err != nil {
		t.Fatalf("创建后台账号失败: %v", err)
	}
	recorder := env.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"identifier": name, "password": password,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("后台登录失败: %d %s", recorder.Code, recorder.Body.String())
	}
	pair := decode[tokenPair](t, recorder)

	if r := env.request(t, http.MethodPatch, "/api/v1/users/@me", pair.AccessToken,
		map[string]string{"display_name": "管理员A"}); r.Code != http.StatusOK {
		t.Fatalf("后台平面 PATCH 失败: %d %s", r.Code, r.Body.String())
	}
	me := decode[model.User](t, env.request(t, http.MethodGet, "/api/v1/users/@me", pair.AccessToken, nil))
	if me.DisplayName != "管理员A" {
		t.Fatalf("后台平面资料未生效: %+v", me)
	}
	// 平面隔离不破坏：client token 打后台端点 401。
	client := env.signup(t)
	if r := env.request(t, http.MethodGet, "/api/v1/users/@me", client.AccessToken, nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("client token 访问后台应 401，实际 %d", r.Code)
	}
}

// ---- 头像上传 / 删除 ----

// minimalPNG 最小合法 PNG 魔数开头的内容（DetectContentType 只看前 512 字节魔数）。
var minimalPNG = append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte{0}, 64)...)

func (env *testEnv) uploadAvatar(t *testing.T, token string, content []byte, filename string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/gapi/v1/users/@me/avatar", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, req)
	return recorder
}

func TestAvatarUploadAndDelete(t *testing.T) {
	env := newEnv(t)
	alice := env.signup(t)

	// 非图片内容拒绝。
	if r := env.uploadAvatar(t, alice.AccessToken, []byte("plain text not an image"), "a.png"); r.Code != http.StatusBadRequest {
		t.Fatalf("非图片应 400，实际 %d %s", r.Code, r.Body.String())
	}

	recorder := env.uploadAvatar(t, alice.AccessToken, minimalPNG, "avatar.png")
	if recorder.Code != http.StatusOK {
		t.Fatalf("上传头像失败: %d %s", recorder.Code, recorder.Body.String())
	}
	result := decode[struct {
		Avatar string     `json:"avatar"`
		User   model.User `json:"user"`
	}](t, recorder)
	if !strings.HasPrefix(result.Avatar, "/public-assets/profile/") || result.User.AvatarURL != result.Avatar {
		t.Fatalf("头像 URL 异常: %+v", result)
	}
	// 磁盘文件存在。
	stored := filepath.Join(env.dataDir, "profile", filepath.Base(result.Avatar))
	if _, err := os.Stat(stored); err != nil {
		t.Fatalf("头像文件未落盘: %v", err)
	}
	env.events.wait(t, "头像 USER_UPDATE", func(e eventbus.Event) bool {
		p, ok := e.Payload.(eventbus.UserUpdatePayload)
		return ok && e.Type == eventbus.EventUserUpdate && p.ID == alice.User.ID && p.Avatar == result.Avatar
	})

	// 删除头像：URL 清空、文件删除。
	if r := env.request(t, http.MethodDelete, "/gapi/v1/users/@me/avatar", alice.AccessToken, nil); r.Code != http.StatusOK {
		t.Fatalf("删除头像失败: %d %s", r.Code, r.Body.String())
	}
	me := decode[model.User](t, env.request(t, http.MethodGet, "/gapi/v1/users/@me", alice.AccessToken, nil))
	if me.AvatarURL != "" {
		t.Fatalf("头像未清除: %q", me.AvatarURL)
	}
	if _, err := os.Stat(stored); !os.IsNotExist(err) {
		t.Fatalf("头像文件未删除: %v", err)
	}
}

// ---- 改密码吊销其他会话 ----

func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	env := newEnv(t)
	first := env.signup(t) // 会话 1（当前会话）
	second := env.login(t, first.User.Username, "password-123") // 会话 2

	// 旧密码错误 → 403，且不吊销任何会话。
	if r := env.request(t, http.MethodPatch, "/gapi/v1/users/@me/password", first.AccessToken,
		map[string]string{"current_password": "wrong-password", "new_password": "new-password-456"}); r.Code != http.StatusForbidden {
		t.Fatalf("旧密码错误应 403，实际 %d", r.Code)
	}

	if r := env.request(t, http.MethodPatch, "/gapi/v1/users/@me/password", first.AccessToken,
		map[string]string{"current_password": "password-123", "new_password": "new-password-456"}); r.Code != http.StatusNoContent {
		t.Fatalf("改密码失败: %d %s", r.Code, r.Body.String())
	}

	// 会话 2 的 refresh token 已失效。
	if r := env.request(t, http.MethodPost, "/gapi/v1/auth/refresh", "",
		map[string]string{"refresh_token": second.RefreshToken}); r.Code != http.StatusUnauthorized {
		t.Fatalf("其他会话 refresh 应 401，实际 %d %s", r.Code, r.Body.String())
	}
	// 当前会话（会话 1）的 refresh token 仍可用。
	if r := env.request(t, http.MethodPost, "/gapi/v1/auth/refresh", "",
		map[string]string{"refresh_token": first.RefreshToken}); r.Code != http.StatusOK {
		t.Fatalf("当前会话 refresh 应存活，实际 %d %s", r.Code, r.Body.String())
	}
	// 新旧密码登录验证。
	if r := env.request(t, http.MethodPost, "/gapi/v1/auth/login", "",
		map[string]string{"identifier": first.User.Username, "password": "password-123"}); r.Code != http.StatusUnauthorized {
		t.Fatalf("旧密码登录应 401，实际 %d", r.Code)
	}
	env.login(t, first.User.Username, "new-password-456")
}

// ---- 会话列表 / 吊销 ----

func TestSessionsListAndRevoke(t *testing.T) {
	env := newEnv(t)
	first := env.signup(t)
	second := env.login(t, first.User.Username, "password-123")

	// refresh 轮换后会话链不变（轮换不产生新会话条目）。
	rotated := decode[tokenPair](t, env.request(t, http.MethodPost, "/gapi/v1/auth/refresh", "",
		map[string]string{"refresh_token": second.RefreshToken}))

	type sessionsResponse struct {
		Sessions []struct {
			ID      uuid.UUID `json:"id"`
			Current bool      `json:"current"`
		} `json:"sessions"`
	}
	list := decode[sessionsResponse](t, env.request(t, http.MethodGet, "/gapi/v1/users/@me/sessions", first.AccessToken, nil))
	if len(list.Sessions) != 2 {
		t.Fatalf("会话数 = %d，期待 2（轮换不得新增会话）", len(list.Sessions))
	}
	var currentID, otherID uuid.UUID
	for _, s := range list.Sessions {
		if s.Current {
			currentID = s.ID
		} else {
			otherID = s.ID
		}
	}
	if currentID == uuid.Nil || otherID == uuid.Nil {
		t.Fatalf("当前会话标记异常: %+v", list.Sessions)
	}

	// 吊销另一会话 → 其轮换后的 refresh token 失效。
	if r := env.request(t, http.MethodDelete, "/gapi/v1/users/@me/sessions/"+otherID.String(), first.AccessToken, nil); r.Code != http.StatusNoContent {
		t.Fatalf("吊销会话失败: %d %s", r.Code, r.Body.String())
	}
	if r := env.request(t, http.MethodPost, "/gapi/v1/auth/refresh", "",
		map[string]string{"refresh_token": rotated.RefreshToken}); r.Code != http.StatusUnauthorized {
		t.Fatalf("被吊销会话 refresh 应 401，实际 %d", r.Code)
	}
	// 再查列表只剩当前会话；重复吊销 → 404。
	list = decode[sessionsResponse](t, env.request(t, http.MethodGet, "/gapi/v1/users/@me/sessions", first.AccessToken, nil))
	if len(list.Sessions) != 1 || !list.Sessions[0].Current {
		t.Fatalf("吊销后会话列表异常: %+v", list.Sessions)
	}
	if r := env.request(t, http.MethodDelete, "/gapi/v1/users/@me/sessions/"+otherID.String(), first.AccessToken, nil); r.Code != http.StatusNotFound {
		t.Fatalf("重复吊销应 404，实际 %d", r.Code)
	}
}

// ---- 公开资料共同 guild 可见性 ----

func TestPublicProfileSharedGuildVisibility(t *testing.T) {
	env := newEnv(t)
	alice := env.signup(t)
	bob := env.signup(t)
	stranger := env.signup(t)

	env.request(t, http.MethodPatch, "/gapi/v1/users/@me", alice.AccessToken,
		map[string]string{"display_name": "小A", "bio": "共同服可见"})

	// 无共同 guild → 404（与不存在的用户一致）。
	if r := env.request(t, http.MethodGet, "/gapi/v1/users/"+alice.User.ID.String(), stranger.AccessToken, nil); r.Code != http.StatusNotFound {
		t.Fatalf("无共同服查资料应 404，实际 %d", r.Code)
	}
	if r := env.request(t, http.MethodGet, "/gapi/v1/users/"+uuid.NewString(), stranger.AccessToken, nil); r.Code != http.StatusNotFound {
		t.Fatalf("不存在用户应 404，实际 %d", r.Code)
	}

	guildID := uuid.New()
	env.joinGuild(t, guildID, alice.User.ID, bob.User.ID)
	recorder := env.request(t, http.MethodGet, "/gapi/v1/users/"+alice.User.ID.String(), bob.AccessToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("共同服查资料失败: %d %s", recorder.Code, recorder.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	// 公开投影不泄露私有字段。
	for _, forbidden := range []string{"email", "system_admin", "password_hash"} {
		if _, ok := raw[forbidden]; ok {
			t.Fatalf("公开资料泄露私有字段 %s: %s", forbidden, recorder.Body.String())
		}
	}
	profile := decode[struct {
		ID          uuid.UUID `json:"id"`
		Username    string    `json:"username"`
		DisplayName string    `json:"display_name"`
		Bio         string    `json:"bio"`
	}](t, recorder)
	if profile.ID != alice.User.ID || profile.DisplayName != "小A" || profile.Bio != "共同服可见" {
		t.Fatalf("公开资料内容异常: %+v", profile)
	}
	// 本人恒可查自己（无需共同服判断）。
	if r := env.request(t, http.MethodGet, "/gapi/v1/users/"+stranger.User.ID.String(), stranger.AccessToken, nil); r.Code != http.StatusOK {
		t.Fatalf("查自己应 200，实际 %d", r.Code)
	}
}

// ---- 用户设置同步 ----

func TestSettingsMergeAndSyncEvent(t *testing.T) {
	env := newEnv(t)
	alice := env.signup(t)

	// 初始为空对象。
	type settingsResponse struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	initial := decode[settingsResponse](t, env.request(t, http.MethodGet, "/gapi/v1/users/@me/settings", alice.AccessToken, nil))
	if len(initial.Settings) != 0 {
		t.Fatalf("初始设置应为空对象: %+v", initial.Settings)
	}

	// 非对象请求体拒绝。
	if r := env.request(t, http.MethodPatch, "/gapi/v1/users/@me/settings", alice.AccessToken, []int{1, 2}); r.Code != http.StatusBadRequest {
		t.Fatalf("数组请求体应 400，实际 %d", r.Code)
	}

	// 首次写入两个顶层 key。
	if r := env.request(t, http.MethodPatch, "/gapi/v1/users/@me/settings", alice.AccessToken, map[string]any{
		"notifications": map[string]any{"global": "mentions", "guilds": map[string]any{}},
		"appearance":    map[string]any{"theme": "dark"},
	}); r.Code != http.StatusOK {
		t.Fatalf("PATCH 设置失败: %d %s", r.Code, r.Body.String())
	}
	// top-level key 合并：替换 notifications、null 删除 appearance、新增 voice。
	if r := env.request(t, http.MethodPatch, "/gapi/v1/users/@me/settings", alice.AccessToken, map[string]any{
		"notifications": map[string]any{"global": "all"},
		"appearance":    nil,
		"voice":         map[string]any{"input_volume": 80},
	}); r.Code != http.StatusOK {
		t.Fatalf("二次 PATCH 失败: %d %s", r.Code, r.Body.String())
	}
	merged := decode[settingsResponse](t, env.request(t, http.MethodGet, "/gapi/v1/users/@me/settings", alice.AccessToken, nil))
	if _, ok := merged.Settings["appearance"]; ok {
		t.Fatalf("null 应删除顶层 key: %+v", merged.Settings)
	}
	if string(merged.Settings["notifications"]) != `{"global": "all"}` && string(merged.Settings["notifications"]) != `{"global":"all"}` {
		t.Fatalf("notifications 应被整体替换: %s", merged.Settings["notifications"])
	}
	if _, ok := merged.Settings["voice"]; !ok {
		t.Fatalf("新增 key 丢失: %+v", merged.Settings)
	}

	// USER_SETTINGS_UPDATE 定向发给本人，载荷为合并后的全量文档。
	env.events.wait(t, "USER_SETTINGS_UPDATE 定向事件", func(e eventbus.Event) bool {
		if e.Type != eventbus.EventUserSettingsUpdate || len(e.UserIDs) != 1 || e.UserIDs[0] != alice.User.ID {
			return false
		}
		p, ok := e.Payload.(eventbus.UserSettingsUpdatePayload)
		if !ok {
			return false
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(p.Settings, &doc); err != nil {
			return false
		}
		_, hasVoice := doc["voice"]
		_, hasAppearance := doc["appearance"]
		return hasVoice && !hasAppearance
	})

	// 大小上限：超过 64KB 拒绝。
	if r := env.request(t, http.MethodPatch, "/gapi/v1/users/@me/settings", alice.AccessToken, map[string]any{
		"huge": strings.Repeat("x", 65*1024),
	}); r.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应 413，实际 %d", r.Code)
	}
}

// TestSettingsPutReplaceWhole PUT /users/@me/settings 整体替换：204、旧顶层 key 全部丢弃、
// USER_SETTINGS_UPDATE 载荷为新全量文档；非对象 400、超限 413。
func TestSettingsPutReplaceWhole(t *testing.T) {
	env := newEnv(t)
	alice := env.signup(t)

	// 先经 PATCH 写入两个 key，再 PUT 全量替换为只含 appearance 的新文档。
	if r := env.request(t, http.MethodPatch, "/gapi/v1/users/@me/settings", alice.AccessToken, map[string]any{
		"notifications": map[string]any{"global": "mentions"},
		"voice":         map[string]any{"input_volume": 80},
	}); r.Code != http.StatusOK {
		t.Fatalf("准备数据 PATCH 失败: %d", r.Code)
	}
	if r := env.request(t, http.MethodPut, "/gapi/v1/users/@me/settings", alice.AccessToken, map[string]any{
		"appearance": map[string]any{"theme": "light"},
	}); r.Code != http.StatusNoContent {
		t.Fatalf("PUT 设置应 204，实际 %d: %s", r.Code, r.Body.String())
	}

	type settingsResponse struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	got := decode[settingsResponse](t, env.request(t, http.MethodGet, "/gapi/v1/users/@me/settings", alice.AccessToken, nil))
	if len(got.Settings) != 1 {
		t.Fatalf("PUT 后应只剩新文档的顶层 key: %+v", got.Settings)
	}
	if _, ok := got.Settings["appearance"]; !ok {
		t.Fatalf("PUT 后缺少 appearance: %+v", got.Settings)
	}

	// USER_SETTINGS_UPDATE 定向本人，载荷为替换后的全量文档。
	env.events.wait(t, "PUT 触发 USER_SETTINGS_UPDATE", func(e eventbus.Event) bool {
		if e.Type != eventbus.EventUserSettingsUpdate || len(e.UserIDs) != 1 || e.UserIDs[0] != alice.User.ID {
			return false
		}
		p, ok := e.Payload.(eventbus.UserSettingsUpdatePayload)
		if !ok {
			return false
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(p.Settings, &doc); err != nil {
			return false
		}
		_, hasAppearance := doc["appearance"]
		_, hasVoice := doc["voice"]
		return hasAppearance && !hasVoice
	})

	// 非对象请求体 400；超限 413。
	if r := env.request(t, http.MethodPut, "/gapi/v1/users/@me/settings", alice.AccessToken, []int{1}); r.Code != http.StatusBadRequest {
		t.Fatalf("数组请求体应 400，实际 %d", r.Code)
	}
	if r := env.request(t, http.MethodPut, "/gapi/v1/users/@me/settings", alice.AccessToken, map[string]any{
		"huge": strings.Repeat("x", 65*1024),
	}); r.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应 413，实际 %d", r.Code)
	}
}
