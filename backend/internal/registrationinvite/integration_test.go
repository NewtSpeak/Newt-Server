package registrationinvite_test

// 集成测试：需要真实 PostgreSQL（覆盖唯一索引、事务内原子消耗等依赖数据库的路径）。
//
// 运行方式（默认跳过，不影响 go test ./...）：
//
//	TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/owl_test?sslmode=disable' go test ./internal/registrationinvite/
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
	"github.com/newtspeak/newt-server/backend/internal/registrationinvite"
	"github.com/newtspeak/newt-server/backend/internal/security"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const integrationSecret = "integration-secret-integration-32"

// newIntegrationRouter 挂后台（/api/v1，含注册邀请管理）、用户端（/gapi/v1）与
// 公开预检（/invite-api），复现生产装配拓扑。
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
	v1 := router.Group("/api/v1")
	api.RegisterRoutes(v1)
	deps := appdeps.Deps{DB: db, Bus: eventbus.New(), Cfg: cfg, Auth: api.AuthMiddleware(), CurrentUser: httpapi.CurrentUser}
	if err := registrationinvite.Register(v1, deps); err != nil {
		t.Fatalf("挂载注册邀请管理 API 失败: %v", err)
	}
	if err := clientapi.Register(router.Group("/gapi/v1"), deps); err != nil {
		t.Fatalf("挂载用户端 API 失败: %v", err)
	}
	if err := registrationinvite.RegisterPublic(router.Group("/invite-api"), deps); err != nil {
		t.Fatalf("挂载公开预检 API 失败: %v", err)
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

// createUser 直接落库造用户并签发后台（aud=admin）access token。
func createUser(t *testing.T, db *gorm.DB, tokens *security.TokenManager, systemAdmin bool) (model.User, string) {
	t.Helper()
	suffix := fmt.Sprintf("%08x", rand.Uint32())
	hash, err := security.HashPassword("password123")
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	user := model.User{ID: uuid.New(), Username: "ri_" + suffix, Email: "ri_" + suffix + "@test.local", PasswordHash: hash, SystemAdmin: systemAdmin}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("插入测试用户失败: %v", err)
	}
	token, _, err := tokens.AccessToken(user.ID)
	if err != nil {
		t.Fatalf("签发后台 token 失败: %v", err)
	}
	return user, token
}

// insertInvite 直接落库造注册邀请（构造过期/用尽等状态）。
func insertInvite(t *testing.T, db *gorm.DB, mutate func(*model.RegistrationInvite)) model.RegistrationInvite {
	t.Helper()
	invite := model.RegistrationInvite{ID: uuid.New(), Code: "ri" + fmt.Sprintf("%08x", rand.Uint32()), CreatedBy: uuid.New()}
	if mutate != nil {
		mutate(&invite)
	}
	if err := db.Create(&invite).Error; err != nil {
		t.Fatalf("插入测试注册邀请失败: %v", err)
	}
	return invite
}

func signupBody(inviteCode string) map[string]string {
	suffix := fmt.Sprintf("%08x", rand.Uint32())
	body := map[string]string{
		"username": "su_" + suffix, "email": "su_" + suffix + "@test.local", "password": "password123",
	}
	if inviteCode != "" {
		body["invite_code"] = inviteCode
	}
	return body
}

// signupGuildBody 构造凭社区（guild）邀请码注册的请求体。
func signupGuildBody(guildInviteCode string) map[string]string {
	body := signupBody("")
	body["guild_invite_code"] = guildInviteCode
	return body
}

// insertGuildInvite 直接落库造 guild 邀请（构造过期/用尽等状态）。
// GuildID 随机即可：signup 只校验邀请本身，不触碰 guild 表。
func insertGuildInvite(t *testing.T, db *gorm.DB, mutate func(*model.Invite)) model.Invite {
	t.Helper()
	invite := model.Invite{ID: uuid.New(), GuildID: uuid.New(), Code: "gi" + fmt.Sprintf("%08x", rand.Uint32()), CreatedBy: uuid.New()}
	if mutate != nil {
		mutate(&invite)
	}
	if err := db.Create(&invite).Error; err != nil {
		t.Fatalf("插入测试 guild 邀请失败: %v", err)
	}
	return invite
}

func errorCode(body map[string]any) string {
	wrapper, ok := body["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := wrapper["code"].(string)
	return code
}

// disableSignupLocally 让「注册开关关闭」仅对本测试进程生效，避免污染共享测试库
// 干扰并行跑的其他包集成测试（message/guildapi 等依赖开放注册）：
//   - 开关权威链为「DB 记录优先，无记录回退 CLIENT_SIGNUP_ENABLED 环境变量」，
//     故此处仅 t.Setenv 置 false（进程本地），并临时摘掉 DB 记录（若有）；
//   - 摘掉 value=true 的记录对其他进程行为中性（无记录时它们的 env 回退仍为 true）。
//
// 必须在 newIntegrationRouter 之前调用（clientapi.Register 挂载时读取环境变量）。
func disableSignupLocally(t *testing.T, db *gorm.DB) {
	t.Helper()
	var original model.PlatformSetting
	if db.First(&original, "key = ?", model.PlatformSettingClientSignup).Error == nil {
		db.Delete(&model.PlatformSetting{}, "key = ?", model.PlatformSettingClientSignup)
		t.Cleanup(func() {
			db.Create(&model.PlatformSetting{Key: original.Key, Value: original.Value})
		})
	}
	t.Setenv("CLIENT_SIGNUP_ENABLED", "false")
}

// TestAdminInviteLifecycle 管理 API 生命周期：非管理员 403 → 创建 → 列表 → 撤销 → 公开预检 410。
func TestAdminInviteLifecycle(t *testing.T) {
	router, db, tokens := newIntegrationRouter(t)
	_, adminToken := createUser(t, db, tokens, true)
	_, userToken := createUser(t, db, tokens, false)

	// 非系统管理员一律 403。
	rec, _ := doJSON(t, router, http.MethodPost, "/api/v1/admin/registration-invites", userToken, map[string]any{})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非管理员创建注册邀请返回 %d，期待 403", rec.Code)
	}

	// 创建：带 TTL 与次数限制。
	rec, created := doJSON(t, router, http.MethodPost, "/api/v1/admin/registration-invites", adminToken, map[string]any{
		"ttl_seconds": 3600, "max_uses": 5,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建注册邀请返回 %d: %s", rec.Code, rec.Body.String())
	}
	code := created["code"].(string)
	inviteID := created["id"].(string)
	if created["status"] != "active" || created["revoked"] != false {
		t.Fatalf("新建邀请状态异常: %v", created)
	}
	shareURL := created["share_url"].(string)
	if !strings.HasSuffix(shareURL, "/register/"+code) {
		t.Fatalf("share_url 拼接异常: %s", shareURL)
	}
	deep := created["deep_link"].(string)
	if !strings.HasPrefix(deep, "owlspeak://register?server=") || !strings.Contains(deep, "code="+code) {
		t.Fatalf("deep_link 拼接异常: %s", deep)
	}

	// 列表包含新建邀请。
	rec, _ = doJSON(t, router, http.MethodGet, "/api/v1/admin/registration-invites", adminToken, nil)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(code)) {
		t.Fatalf("列表缺少新建邀请（%d）: %s", rec.Code, rec.Body.String())
	}

	// 公开预检：有效邀请返回 200 与剩余次数。
	rec, info := doJSON(t, router, http.MethodGet, "/invite-api/registration/"+code, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("公开预检返回 %d: %s", rec.Code, rec.Body.String())
	}
	if info["remaining_uses"] != float64(5) || info["server_name"] == "" {
		t.Fatalf("公开预检字段异常: %v", info)
	}

	// 撤销（幂等）→ 列表状态 revoked → 公开预检 410。
	for i := 0; i < 2; i++ {
		rec, _ = doJSON(t, router, http.MethodDelete, "/api/v1/admin/registration-invites/"+inviteID, adminToken, nil)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("撤销注册邀请返回 %d（第 %d 次）", rec.Code, i+1)
		}
	}
	var revoked model.RegistrationInvite
	if err := db.First(&revoked, "id = ?", inviteID).Error; err != nil || revoked.RevokedAt == nil {
		t.Fatalf("撤销后 RevokedAt 未写入: %v %v", err, revoked.RevokedAt)
	}
	rec, _ = doJSON(t, router, http.MethodGet, "/invite-api/registration/"+code, "", nil)
	if rec.Code != http.StatusGone {
		t.Fatalf("撤销后公开预检返回 %d，期待 410", rec.Code)
	}
}

// TestPublicInviteStates 公开预检各状态码：不存在 404，过期/用尽 410，不限次 remaining_uses=null。
func TestPublicInviteStates(t *testing.T) {
	router, db, _ := newIntegrationRouter(t)

	rec, _ := doJSON(t, router, http.MethodGet, "/invite-api/registration/nonexistent0", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的邀请码返回 %d，期待 404", rec.Code)
	}

	expired := insertInvite(t, db, func(invite *model.RegistrationInvite) {
		past := time.Now().UTC().Add(-time.Hour)
		invite.ExpiresAt = &past
	})
	rec, _ = doJSON(t, router, http.MethodGet, "/invite-api/registration/"+expired.Code, "", nil)
	if rec.Code != http.StatusGone {
		t.Fatalf("过期邀请返回 %d，期待 410", rec.Code)
	}

	exhausted := insertInvite(t, db, func(invite *model.RegistrationInvite) {
		invite.MaxUses, invite.Uses = 1, 1
	})
	rec, _ = doJSON(t, router, http.MethodGet, "/invite-api/registration/"+exhausted.Code, "", nil)
	if rec.Code != http.StatusGone {
		t.Fatalf("用尽邀请返回 %d，期待 410", rec.Code)
	}

	unlimited := insertInvite(t, db, nil)
	rec, info := doJSON(t, router, http.MethodGet, "/invite-api/registration/"+unlimited.Code, "", nil)
	if rec.Code != http.StatusOK || info["remaining_uses"] != nil {
		t.Fatalf("不限次邀请预检异常（%d）: %v", rec.Code, info)
	}
}

// TestSignupWithInvite 注册开关关闭时：无码 403、凭有效码放行；一次性码二次使用被拒；
// 无效码 403 INVITE_INVALID。
func TestSignupWithInvite(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过数据库集成测试（说明见文件头注释）")
	}
	setupDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	// 先关注册开关再挂路由（clientapi.Register 挂载时读取环境变量回退值）。
	disableSignupLocally(t, setupDB)
	router, db, _ := newIntegrationRouter(t)

	// 开关关闭 + 无邀请码 → SIGNUP_DISABLED。
	rec, body := doJSON(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", signupBody(""))
	if rec.Code != http.StatusForbidden || errorCode(body) != "SIGNUP_DISABLED" {
		t.Fatalf("开关关闭时无码注册返回 %d/%s，期待 403/SIGNUP_DISABLED", rec.Code, errorCode(body))
	}

	// 无效邀请码 → INVITE_INVALID。
	rec, body = doJSON(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", signupBody("nosuchcode"))
	if rec.Code != http.StatusForbidden || errorCode(body) != "INVITE_INVALID" {
		t.Fatalf("无效邀请码注册返回 %d/%s，期待 403/INVITE_INVALID", rec.Code, errorCode(body))
	}

	// 一次性有效码 → 开关关闭也放行。
	oneTime := insertInvite(t, db, func(invite *model.RegistrationInvite) { invite.MaxUses = 1 })
	rec, body = doJSON(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", signupBody(oneTime.Code))
	if rec.Code != http.StatusCreated {
		t.Fatalf("凭有效邀请码注册返回 %d: %s", rec.Code, rec.Body.String())
	}
	if user, ok := body["user"].(map[string]any); !ok || user["system_admin"] != false {
		t.Fatalf("邀请注册的用户不应是系统管理员: %v", body)
	}
	var used model.RegistrationInvite
	if err := db.First(&used, "id = ?", oneTime.ID).Error; err != nil || used.Uses != 1 {
		t.Fatalf("邀请消耗计数异常: %v uses=%d", err, used.Uses)
	}

	// 一次性码二次使用 → INVITE_EXPIRED。
	rec, body = doJSON(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", signupBody(oneTime.Code))
	if rec.Code != http.StatusForbidden || errorCode(body) != "INVITE_EXPIRED" {
		t.Fatalf("一次性码二次使用返回 %d/%s，期待 403/INVITE_EXPIRED", rec.Code, errorCode(body))
	}

	// 已撤销码 → INVITE_EXPIRED。
	revoked := insertInvite(t, db, func(invite *model.RegistrationInvite) {
		now := time.Now().UTC()
		invite.RevokedAt = &now
	})
	rec, body = doJSON(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", signupBody(revoked.Code))
	if rec.Code != http.StatusForbidden || errorCode(body) != "INVITE_EXPIRED" {
		t.Fatalf("已撤销码注册返回 %d/%s，期待 403/INVITE_EXPIRED", rec.Code, errorCode(body))
	}
}

// TestSignupWithGuildInvite 注册开关关闭时凭社区（guild）邀请码注册：
// 有效码放行且不消耗次数（次数由后续 join 消耗）；不存在 INVITE_INVALID；
// 过期/用尽 INVITE_EXPIRED。
func TestSignupWithGuildInvite(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过数据库集成测试（说明见文件头注释）")
	}
	setupDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	// 先关注册开关再挂路由（clientapi.Register 挂载时读取环境变量回退值）。
	disableSignupLocally(t, setupDB)
	router, db, _ := newIntegrationRouter(t)

	// 不存在的 guild 码 → INVITE_INVALID。
	rec, body := doJSON(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", signupGuildBody("nosuchguildcode"))
	if rec.Code != http.StatusForbidden || errorCode(body) != "INVITE_INVALID" {
		t.Fatalf("不存在 guild 码注册返回 %d/%s，期待 403/INVITE_INVALID", rec.Code, errorCode(body))
	}

	// 过期的 guild 码 → INVITE_EXPIRED。
	expired := insertGuildInvite(t, db, func(invite *model.Invite) {
		past := time.Now().UTC().Add(-time.Hour)
		invite.ExpiresAt = &past
	})
	rec, body = doJSON(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", signupGuildBody(expired.Code))
	if rec.Code != http.StatusForbidden || errorCode(body) != "INVITE_EXPIRED" {
		t.Fatalf("过期 guild 码注册返回 %d/%s，期待 403/INVITE_EXPIRED", rec.Code, errorCode(body))
	}

	// 已用尽的 guild 码 → INVITE_EXPIRED。
	exhausted := insertGuildInvite(t, db, func(invite *model.Invite) {
		invite.MaxUses, invite.Uses = 1, 1
	})
	rec, body = doJSON(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", signupGuildBody(exhausted.Code))
	if rec.Code != http.StatusForbidden || errorCode(body) != "INVITE_EXPIRED" {
		t.Fatalf("用尽 guild 码注册返回 %d/%s，期待 403/INVITE_EXPIRED", rec.Code, errorCode(body))
	}

	// 有效 guild 码（限 1 次）→ 开关关闭也放行，且 signup 本身不消耗次数。
	valid := insertGuildInvite(t, db, func(invite *model.Invite) { invite.MaxUses = 1 })
	rec, body = doJSON(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", signupGuildBody(valid.Code))
	if rec.Code != http.StatusCreated {
		t.Fatalf("凭有效 guild 码注册返回 %d: %s", rec.Code, rec.Body.String())
	}
	if user, ok := body["user"].(map[string]any); !ok || user["system_admin"] != false {
		t.Fatalf("guild 码注册的用户不应是系统管理员: %v", body)
	}
	var after model.Invite
	if err := db.First(&after, "id = ?", valid.ID).Error; err != nil || after.Uses != 0 {
		t.Fatalf("signup 不应消耗 guild 邀请次数: %v uses=%d", err, after.Uses)
	}

	// 同一有效码可重复用于注册（次数只在 join 时消耗）。
	rec, _ = doJSON(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", signupGuildBody(valid.Code))
	if rec.Code != http.StatusCreated {
		t.Fatalf("同一 guild 码二次注册返回 %d: %s", rec.Code, rec.Body.String())
	}

	// invite_code 与 guild_invite_code 同时提供 → invite_code（注册邀请）优先：
	// 提供无效注册邀请码 + 有效 guild 码，应按注册邀请报 INVITE_INVALID。
	both := signupGuildBody(valid.Code)
	both["invite_code"] = "nosuchregcode"
	rec, body = doJSON(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", both)
	if rec.Code != http.StatusForbidden || errorCode(body) != "INVITE_INVALID" {
		t.Fatalf("双码并存应以注册邀请优先，返回 %d/%s，期待 403/INVITE_INVALID", rec.Code, errorCode(body))
	}
}

// TestSignupInviteConcurrency 一次性码并发注册不超用：恰好一个 201，其余 403。
func TestSignupInviteConcurrency(t *testing.T) {
	router, db, _ := newIntegrationRouter(t)
	oneTime := insertInvite(t, db, func(invite *model.RegistrationInvite) { invite.MaxUses = 1 })

	const workers = 4
	results := make([]int, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			raw, _ := json.Marshal(signupBody(oneTime.Code))
			req := httptest.NewRequest(http.MethodPost, "/gapi/v1/auth/signup", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			results[slot] = rec.Code
		}(i)
	}
	wg.Wait()

	created := 0
	for _, code := range results {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusForbidden:
		default:
			t.Fatalf("并发注册出现意外状态码: %v", results)
		}
	}
	if created != 1 {
		t.Fatalf("一次性码并发注册成功 %d 次，期待恰好 1 次: %v", created, results)
	}
	var final model.RegistrationInvite
	if err := db.First(&final, "id = ?", oneTime.ID).Error; err != nil || final.Uses != 1 {
		t.Fatalf("并发后消耗计数异常: %v uses=%d", err, final.Uses)
	}
}
