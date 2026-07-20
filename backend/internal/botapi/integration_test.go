package botapi_test

// Bot 开放平台全链路集成测试：需要真实 PostgreSQL。
//
// 运行方式（默认跳过，不影响 go test ./...）：
//
//	TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/owl_test?sslmode=disable' go test ./internal/botapi/
//
// 复现生产装配拓扑：后台 /api/v1（httpapi + botapi.Register）与 bot 开放平面
// /bot-api/v1（botapi.RegisterBotAPI，内部复用 message.RegisterBot）挂同一 router。
// 覆盖：创建 bot / 签发 token → 安装到服 → bot token 鉴权（/me、guilds）→
// 发文本与卡片消息（author_is_bot / card 回读）→ 流式三段协议（事件计数与正文拼接）→
// 令牌吊销 401 → 卸载后 404。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/botapi"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/httpapi"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/security"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newBotRouter(t *testing.T) (*gin.Engine, *gorm.DB, *eventbus.Bus) {
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
	cfg := config.Config{
		JWTSecret:       "integration-secret-integration-32",
		AccessTokenTTL:  time.Minute,
		RefreshTokenTTL: time.Hour,
		DataDir:         t.TempDir(),
	}
	tokens := security.NewTokenManager(cfg.JWTSecret, cfg.AccessTokenTTL)
	router := gin.New()
	api := httpapi.New(db, tokens, cfg.RefreshTokenTTL)
	v1 := router.Group("/api/v1")
	api.RegisterRoutes(v1)
	bus := eventbus.New()
	deps := appdeps.Deps{DB: db, Bus: bus, Cfg: cfg, Auth: api.AuthMiddleware(), CurrentUser: httpapi.CurrentUser}
	if err := botapi.Register(v1, deps); err != nil {
		t.Fatalf("挂载后台 bot 管理 API 失败: %v", err)
	}
	if err := botapi.RegisterBotAPI(router.Group("/bot-api"), deps); err != nil {
		t.Fatalf("挂载 bot 开放 API 失败: %v", err)
	}
	return router, db, bus
}

// authHeader 支持 "Bearer x"（管理员）与 "Bot x"（机器人）两种形态。
func doBotReq(t *testing.T, router *gin.Engine, method, path, authHeader string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			t.Fatalf("序列化请求失败: %v", err)
		}
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

// setupAdmin 直接落库创建系统管理员并经 /api/v1/auth/login 换取后台 token。
func setupAdmin(t *testing.T, router *gin.Engine, db *gorm.DB) string {
	t.Helper()
	suffix := fmt.Sprintf("%08x", rand.Uint32())
	hash, err := security.HashPassword("password123")
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	admin := model.User{
		ID: uuid.New(), Username: "botadmin_" + suffix,
		Email: "botadmin_" + suffix + "@test.local", PasswordHash: hash, SystemAdmin: true,
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}
	rec, body := doBotReq(t, router, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"identifier": admin.Username, "password": "password123",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("管理员登录返回 %d: %s", rec.Code, rec.Body.String())
	}
	return "Bearer " + body["access_token"].(string)
}

func TestBotFullFlow(t *testing.T) {
	router, db, bus := newBotRouter(t)
	adminAuth := setupAdmin(t, router, db)
	suffix := fmt.Sprintf("%08x", rand.Uint32())

	// 订阅流式事件计数。
	var mu sync.Mutex
	streamEvents := map[string]int{}
	bus.Subscribe(func(event eventbus.Event) {
		switch event.Type {
		case eventbus.EventMessageStreamStart, eventbus.EventMessageStreamDelta, eventbus.EventMessageStreamEnd:
			mu.Lock()
			streamEvents[event.Type]++
			mu.Unlock()
		}
	})

	// 1. 创建 bot。
	rec, bot := doBotReq(t, router, http.MethodPost, "/api/v1/bots", adminAuth, map[string]string{
		"name": "AI 助手 " + suffix, "username": "aibot_" + suffix, "description": "集成测试机器人",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建 bot 返回 %d: %s", rec.Code, rec.Body.String())
	}
	botID := bot["id"].(string)

	// 2. 签发 token（明文仅此一次）。
	rec, issued := doBotReq(t, router, http.MethodPost, "/api/v1/bots/"+botID+"/tokens", adminAuth, map[string]string{"name": "ci"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("签发 token 返回 %d: %s", rec.Code, rec.Body.String())
	}
	plain := issued["plain"].(string)
	botAuth := "Bot " + plain

	// 3. 建服 + 建频道（频道直接落库）。
	rec, guild := doBotReq(t, router, http.MethodPost, "/api/v1/guilds", adminAuth, map[string]string{"name": "bot 集成服 " + suffix})
	if rec.Code != http.StatusCreated {
		t.Fatalf("建服返回 %d: %s", rec.Code, rec.Body.String())
	}
	guildID := guild["id"].(string)
	channel := model.Channel{ID: uuid.New(), GuildID: uuid.MustParse(guildID), Name: "general", Type: model.ChannelText}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("插入测试频道失败: %v", err)
	}

	// 4. 未安装前：bot 对该服不可见（404 防扫频）。
	rec, _ = doBotReq(t, router, http.MethodGet, "/bot-api/v1/guilds/"+guildID+"/channels", botAuth, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未安装时读频道应 404，实际 %d", rec.Code)
	}

	// 5. 安装到服。
	rec, _ = doBotReq(t, router, http.MethodPut, "/api/v1/guilds/"+guildID+"/bots/"+botID, adminAuth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("安装 bot 返回 %d: %s", rec.Code, rec.Body.String())
	}

	// 6. bot token 鉴权：/me 与 /guilds。
	rec, me := doBotReq(t, router, http.MethodGet, "/bot-api/v1/me", botAuth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/me 返回 %d: %s", rec.Code, rec.Body.String())
	}
	if user := me["user"].(map[string]any); user["is_bot"] != true {
		t.Errorf("bot 用户 is_bot 应为 true：%v", user)
	}
	rec, myGuilds := doBotReq(t, router, http.MethodGet, "/bot-api/v1/guilds", botAuth, nil)
	if rec.Code != http.StatusOK || len(myGuilds["guilds"].([]any)) != 1 {
		t.Fatalf("bot guilds 返回 %d: %s", rec.Code, rec.Body.String())
	}

	// 7. 发送卡片消息：card 回读、author_is_bot 标记。
	base := "/bot-api/v1/channels/" + channel.ID.String()
	card := map[string]any{"title": "部署完成", "color": "#22c55e"}
	rec, msg := doBotReq(t, router, http.MethodPost, base+"/messages", botAuth, map[string]any{
		"content": "带卡片的消息", "card": card,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("bot 发消息返回 %d: %s", rec.Code, rec.Body.String())
	}
	if msg["author_is_bot"] != true {
		t.Errorf("author_is_bot 应为 true：%v", msg["author_is_bot"])
	}
	if returned, ok := msg["card"].(map[string]any); !ok || returned["title"] != "部署完成" {
		t.Errorf("card 未正确回读：%v", msg["card"])
	}

	// 8. 流式三段协议。
	rec, streamMsg := doBotReq(t, router, http.MethodPost, base+"/messages/stream", botAuth, map[string]string{"content": "思考中："})
	if rec.Code != http.StatusCreated {
		t.Fatalf("开始流式返回 %d: %s", rec.Code, rec.Body.String())
	}
	streamID := streamMsg["id"].(string)
	if streamMsg["stream_status"] != "STREAMING" {
		t.Errorf("流式占位消息 stream_status=%v", streamMsg["stream_status"])
	}
	for i, delta := range []string{"答案", "是 42。"} {
		rec, appendResp := doBotReq(t, router, http.MethodPost, base+"/messages/"+streamID+"/stream", botAuth, map[string]string{"delta": delta})
		if rec.Code != http.StatusOK {
			t.Fatalf("追加分片返回 %d: %s", rec.Code, rec.Body.String())
		}
		if seq := int(appendResp["seq"].(float64)); seq != i+1 {
			t.Errorf("分片 seq=%d，期待 %d", seq, i+1)
		}
	}
	rec, final := doBotReq(t, router, http.MethodPost, base+"/messages/"+streamID+"/stream/end", botAuth, map[string]any{
		"card": map[string]any{"footer": "AI Bot"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("结束流式返回 %d: %s", rec.Code, rec.Body.String())
	}
	if final["content"] != "思考中：答案是 42。" {
		t.Errorf("流式正文拼接结果不符：%v", final["content"])
	}
	if final["stream_status"] != nil && final["stream_status"] != "" {
		t.Errorf("终态 stream_status 应清空：%v", final["stream_status"])
	}
	// 二次 end 应 409（NOT_STREAMING）。
	rec, _ = doBotReq(t, router, http.MethodPost, base+"/messages/"+streamID+"/stream/end", botAuth, map[string]any{})
	if rec.Code != http.StatusConflict {
		t.Errorf("重复 end 应 409，实际 %d", rec.Code)
	}
	mu.Lock()
	if streamEvents[eventbus.EventMessageStreamStart] != 1 || streamEvents[eventbus.EventMessageStreamDelta] != 2 || streamEvents[eventbus.EventMessageStreamEnd] != 1 {
		t.Errorf("流式事件计数不符：%v", streamEvents)
	}
	mu.Unlock()

	// 9. 吊销 token 后 401。
	rec, tokensResp := doBotReq(t, router, http.MethodGet, "/api/v1/bots/"+botID+"/tokens", adminAuth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("列令牌返回 %d", rec.Code)
	}
	tokenID := tokensResp["tokens"].([]any)[0].(map[string]any)["id"].(string)
	rec, _ = doBotReq(t, router, http.MethodDelete, "/api/v1/bots/"+botID+"/tokens/"+tokenID, adminAuth, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("吊销 token 返回 %d", rec.Code)
	}
	rec, _ = doBotReq(t, router, http.MethodGet, "/bot-api/v1/me", botAuth, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("吊销后 /me 应 401，实际 %d", rec.Code)
	}

	// 10. 重新签发并卸载：卸载后频道不可见（404）。
	rec, reissued := doBotReq(t, router, http.MethodPost, "/api/v1/bots/"+botID+"/tokens", adminAuth, map[string]string{"name": "ci-2"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("重签 token 返回 %d", rec.Code)
	}
	botAuth = "Bot " + reissued["plain"].(string)
	rec, _ = doBotReq(t, router, http.MethodDelete, "/api/v1/guilds/"+guildID+"/bots/"+botID, adminAuth, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("卸载 bot 返回 %d: %s", rec.Code, rec.Body.String())
	}
	rec, _ = doBotReq(t, router, http.MethodPost, base+"/messages", botAuth, map[string]string{"content": "卸载后不应可发"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("卸载后发消息应 404，实际 %d", rec.Code)
	}
}
