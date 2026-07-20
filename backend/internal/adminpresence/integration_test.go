package adminpresence_test

// 系统管理员临场 / 音频审计 + 密钥同步全链路集成测试（需要真实 PostgreSQL）。
//
//	TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/owl_test?sslmode=disable' go test ./internal/adminpresence/
//
// 覆盖：平台/频道审计配置继承与覆盖、审计 predicate 注入 voice、管理员临场文本发言、
// 语音隐身开关、审计录音上传（/audit-api Bearer 鉴权）与列表；
// 以及 keysync 保险库读写、版本乐观锁冲突。

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
	"github.com/owlspeak/owl-server/backend/internal/adminpresence"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/httpapi"
	"github.com/owlspeak/owl-server/backend/internal/keysync"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/security"
	"github.com/owlspeak/owl-server/backend/internal/voice"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newRouter(t *testing.T) (*gin.Engine, *gorm.DB, config.Config) {
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
	cfg := config.Config{
		JWTSecret: "integration-secret-integration-32", AccessTokenTTL: time.Minute,
		RefreshTokenTTL: time.Hour, DataDir: t.TempDir(), AuditIngestToken: "ingest-secret",
	}
	tokens := security.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL)
	router := gin.New()
	api := httpapi.New(db, tokens, cfg.RefreshTokenTTL)
	v1 := router.Group("/api/v1")
	api.RegisterRoutes(v1)
	bus := eventbus.New()
	deps := appdeps.Deps{DB: db, Bus: bus, Cfg: cfg, Auth: api.AuthMiddleware(), CurrentUser: httpapi.CurrentUser}
	if err := adminpresence.Register(v1, deps); err != nil {
		t.Fatalf("挂载 adminpresence 失败: %v", err)
	}
	if err := adminpresence.RegisterIngest(router.Group("/audit-api"), deps); err != nil {
		t.Fatalf("挂载 audit-api 失败: %v", err)
	}
	// keysync 直接挂到测试用 /gapi/v1（避免依赖 clientapi → guildapi 的并行开发状态）；
	// 认证中间件解析 aud=client token，与生产 clientapi 语义一致。
	gapiTokens := security.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL)
	clientDeps := deps
	clientDeps.Auth = testClientAuth(db, gapiTokens)
	clientDeps.CurrentUser = testClientUser
	gapi := router.Group("/gapi/v1")
	gapi.POST("/auth/signup", func(c *gin.Context) { testSignup(c, db, gapiTokens) })
	keysync.RegisterClient(gapi.Group("", clientDeps.Auth), clientDeps)
	return router, db, cfg
}

// testClientUserKey 测试用客户端上下文键。
const testClientUserKey = "test_client_user"

func testClientAuth(db *gorm.DB, tokens *security.TokenManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if len(header) < 8 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
			return
		}
		userID, err := tokens.ParseAccessTokenWithAudience(header[7:], security.AudienceClient)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
			return
		}
		var user model.User
		if err := db.First(&user, "id = ?", userID).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED"}})
			return
		}
		c.Set(testClientUserKey, user)
		c.Next()
	}
}

func testClientUser(c *gin.Context) model.User { return c.MustGet(testClientUserKey).(model.User) }

func testSignup(c *gin.Context, db *gorm.DB, tokens *security.TokenManager) {
	var in struct{ Username, Email, Password string }
	_ = c.ShouldBindJSON(&in)
	hash, _ := security.HashPassword(in.Password)
	user := model.User{ID: uuid.New(), Username: in.Username, Email: in.Email, PasswordHash: hash}
	if err := db.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "DB"}})
		return
	}
	access, _, _ := tokens.AccessTokenWithAudience(user.ID, security.AudienceClient)
	c.JSON(http.StatusCreated, gin.H{"access_token": access, "user": user})
}

func doJSON(t *testing.T, router *gin.Engine, method, path, authHeader string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	parsed := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec, parsed
}

func adminToken(t *testing.T, router *gin.Engine, db *gorm.DB) (string, model.User) {
	t.Helper()
	suffix := fmt.Sprintf("%08x", rand.Uint32())
	hash, _ := security.HashPassword("password123")
	admin := model.User{ID: uuid.New(), Username: "ap_" + suffix, Email: "ap_" + suffix + "@t.local", PasswordHash: hash, SystemAdmin: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("建管理员失败: %v", err)
	}
	rec, body := doJSON(t, router, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"identifier": admin.Username, "password": "password123"})
	if rec.Code != http.StatusOK {
		t.Fatalf("登录失败 %d: %s", rec.Code, rec.Body.String())
	}
	return "Bearer " + body["access_token"].(string), admin
}

func TestAdminPresenceAndAudit(t *testing.T) {
	router, db, _ := newRouter(t)
	auth, admin := adminToken(t, router, db)
	suffix := fmt.Sprintf("%08x", rand.Uint32())

	// 建服 + 文本频道 + 语音频道。
	rec, guild := doJSON(t, router, http.MethodPost, "/api/v1/guilds", auth, map[string]string{"name": "临场服 " + suffix})
	if rec.Code != http.StatusCreated {
		t.Fatalf("建服失败 %d: %s", rec.Code, rec.Body.String())
	}
	guildID := uuid.MustParse(guild["id"].(string))
	textCh := model.Channel{ID: uuid.New(), GuildID: guildID, Name: "general", Type: model.ChannelText}
	voiceCh := model.Channel{ID: uuid.New(), GuildID: guildID, Name: "voice", Type: model.ChannelVoice}
	if err := db.Create(&textCh).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&voiceCh).Error; err != nil {
		t.Fatal(err)
	}

	// 1. 平台审计默认：开启录制、关闭提示（静默审计）。
	rec, _ = doJSON(t, router, http.MethodPut, "/api/v1/admin/audit-config", auth, map[string]bool{"record_default": true, "notify_default": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("平台审计配置失败 %d: %s", rec.Code, rec.Body.String())
	}
	// voice predicate 应反映：录制 true、提示 false。
	if !voice.AuditPredicate(guildID, voiceCh.ID) {
		t.Error("AuditPredicate 应为 true（继承平台默认）")
	}
	if voice.AuditNotifyPredicate(guildID, voiceCh.ID) {
		t.Error("AuditNotifyPredicate 应为 false（平台默认静默）")
	}

	// 2. 频道独立覆盖：该频道不录制。
	rec, ch := doJSON(t, router, http.MethodPut, "/api/v1/admin/channels/"+voiceCh.ID.String()+"/audit-config", auth, map[string]any{"record": false, "notify": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("频道审计配置失败 %d: %s", rec.Code, rec.Body.String())
	}
	if ch["record"] != false {
		t.Errorf("频道覆盖后 record 应为 false：%v", ch["record"])
	}
	if voice.AuditPredicate(guildID, voiceCh.ID) {
		t.Error("频道覆盖后 AuditPredicate 应为 false")
	}

	// 3. 管理员临场文本发言。
	rec, msg := doJSON(t, router, http.MethodPost, "/api/v1/admin/channels/"+textCh.ID.String()+"/presence/message", auth, map[string]string{"content": "管理员在此发言"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("临场发言失败 %d: %s", rec.Code, rec.Body.String())
	}
	if msg["author_id"] != admin.ID.String() {
		t.Errorf("发言作者应为管理员：%v", msg["author_id"])
	}
	var msgCount int64
	db.Model(&model.Message{}).Where("channel_id = ?", textCh.ID).Count(&msgCount)
	if msgCount != 1 {
		t.Errorf("应落库 1 条消息，实际 %d", msgCount)
	}

	// 4. 语音隐身开关。
	rec, _ = doJSON(t, router, http.MethodPut, "/api/v1/admin/voice/stealth", auth, map[string]any{"guild_id": guildID, "hidden": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("设置隐身失败 %d: %s", rec.Code, rec.Body.String())
	}
	if !voice.StealthPredicate(guildID, admin.ID) {
		t.Error("StealthPredicate 应为 true")
	}
	rec, st := doJSON(t, router, http.MethodGet, "/api/v1/admin/voice/stealth?guild_id="+guildID.String(), auth, nil)
	if rec.Code != http.StatusOK || st["hidden"] != true {
		t.Errorf("查询隐身应为 true：%d %v", rec.Code, st["hidden"])
	}

	// 5. 审计录音上传（/audit-api，Bearer 共享密钥）+ 列表。
	ingestPath := fmt.Sprintf("/audit-api/records?guild_id=%s&channel_id=%s&user_id=%s&session_id=s1&node_id=n1&started=%d&ended=%d",
		guildID, voiceCh.ID, admin.ID, time.Now().Unix()-10, time.Now().Unix())
	req := httptest.NewRequest(http.MethodPost, ingestPath, bytes.NewReader([]byte("OggS-fake-audio-bytes")))
	req.Header.Set("Authorization", "Bearer ingest-secret")
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("审计上传失败 %d: %s", rec2.Code, rec2.Body.String())
	}
	// 错误密钥应 401。
	reqBad := httptest.NewRequest(http.MethodPost, ingestPath, bytes.NewReader([]byte("x")))
	reqBad.Header.Set("Authorization", "Bearer wrong")
	recBad := httptest.NewRecorder()
	router.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusUnauthorized {
		t.Errorf("错误密钥应 401，实际 %d", recBad.Code)
	}
	rec, list := doJSON(t, router, http.MethodGet, "/api/v1/admin/audit-records?guild_id="+guildID.String(), auth, nil)
	if rec.Code != http.StatusOK || len(list["records"].([]any)) != 1 {
		t.Fatalf("审计列表应有 1 条：%d %s", rec.Code, rec.Body.String())
	}
}

func TestKeySyncVault(t *testing.T) {
	router, db, _ := newRouter(t)
	// 用户端账号（普通用户）。
	suffix := fmt.Sprintf("%08x", rand.Uint32())
	rec, signup := doJSON(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", map[string]string{
		"username": "vault_" + suffix, "email": "vault_" + suffix + "@t.local", "password": "password123",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("注册失败 %d: %s", rec.Code, rec.Body.String())
	}
	userAuth := "Bearer " + signup["access_token"].(string)
	_ = db

	// 初始为空（version 0）。
	rec, v := doJSON(t, router, http.MethodGet, "/gapi/v1/users/@me/vault", userAuth, nil)
	if rec.Code != http.StatusOK || v["version"].(float64) != 0 {
		t.Fatalf("初始保险库应 version=0：%d %v", rec.Code, v["version"])
	}

	// 首次写入 base_version=0 → version=1。
	rec, v = doJSON(t, router, http.MethodPut, "/gapi/v1/users/@me/vault", userAuth, map[string]any{
		"ciphertext": "enc-blob-1", "nonce": "n1", "kdf_salt": "s1", "algo": "xchacha20poly1305-argon2id",
		"base_version": 0, "device_id": "desktop",
	})
	if rec.Code != http.StatusOK || v["version"].(float64) != 1 {
		t.Fatalf("首次写入应 version=1：%d %v", rec.Code, v["version"])
	}

	// 用陈旧 base_version=0 再写 → 409 冲突。
	rec, _ = doJSON(t, router, http.MethodPut, "/gapi/v1/users/@me/vault", userAuth, map[string]any{
		"ciphertext": "enc-blob-stale", "base_version": 0, "device_id": "phone",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("陈旧版本写入应 409，实际 %d", rec.Code)
	}

	// 用正确 base_version=1 写 → version=2。
	rec, v = doJSON(t, router, http.MethodPut, "/gapi/v1/users/@me/vault", userAuth, map[string]any{
		"ciphertext": "enc-blob-2", "base_version": 1, "device_id": "phone",
	})
	if rec.Code != http.StatusOK || v["version"].(float64) != 2 || v["ciphertext"] != "enc-blob-2" {
		t.Fatalf("正确版本写入应 version=2：%d %v", rec.Code, rec.Body.String())
	}
}
