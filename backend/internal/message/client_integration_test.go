package message_test

// 用户端（/gapi/v1）文本频道全链路集成测试：需要真实 PostgreSQL。
//
// 运行方式（默认跳过，不影响 go test ./...）：
//
//	TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/owl_test?sslmode=disable' go test ./internal/message/
//
// 复现生产装配拓扑：后台 /api/v1（httpapi + message.Register）与用户端 /gapi/v1
//（clientapi.Register，内部经 text.go 钩子调用 message.RegisterClient）挂同一 router，
// 覆盖：消息收发/编辑/反应/附件二段式上传/签名下载/搜索/typing/author_username，
// 以及本专项安全要求——用户端响应中的 URL 一律 /gapi/v1 前缀、绝不出现 /api/v1。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"github.com/newtspeak/newt-server/backend/internal/message"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/security"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newTextRouter 装配后台 + 用户端双前缀 router（生产拓扑），返回 router、DB 与事件总线。
func newTextRouter(t *testing.T) (*gin.Engine, *gorm.DB, *eventbus.Bus) {
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
	// 后台平面：与 server.New 一致挂 message.Register。
	if err := message.Register(v1, deps); err != nil {
		t.Fatalf("挂载后台消息 API 失败: %v", err)
	}
	// 用户端平面：clientapi.Register 内部经钩子调用 message.RegisterClient。
	if err := clientapi.Register(router.Group("/gapi/v1"), deps); err != nil {
		t.Fatalf("挂载用户端 API 失败: %v", err)
	}
	return router, db, bus
}

func doReq(t *testing.T, router *gin.Engine, method, path, token string, body []byte, contentType string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	parsed := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec, parsed
}

func doJSONReq(t *testing.T, router *gin.Engine, method, path, token string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			t.Fatalf("序列化请求失败: %v", err)
		}
	}
	return doReq(t, router, method, path, token, raw, "application/json")
}

// setupTextFixture 注册用户 → 建服 → 直接落库建一个文本频道，返回 token、用户名与频道 ID。
func setupTextFixture(t *testing.T, router *gin.Engine, db *gorm.DB) (token, username string, channelID uuid.UUID) {
	t.Helper()
	suffix := fmt.Sprintf("%08x", rand.Uint32())
	username = "msg_" + suffix
	rec, body := doJSONReq(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", map[string]string{
		"username": username, "email": username + "@test.local", "password": "password123",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("注册返回 %d: %s", rec.Code, rec.Body.String())
	}
	token = body["access_token"].(string)
	rec, guild := doJSONReq(t, router, http.MethodPost, "/gapi/v1/guilds", token, map[string]string{"name": "文本集成服 " + suffix})
	if rec.Code != http.StatusCreated {
		t.Fatalf("建服返回 %d: %s", rec.Code, rec.Body.String())
	}
	guildID, err := uuid.Parse(guild["id"].(string))
	if err != nil {
		t.Fatalf("解析 guild id 失败: %v", err)
	}
	channel := model.Channel{
		ID: uuid.New(), GuildID: guildID, Name: "general", Type: model.ChannelText,
		AllowRestrictedVisibility: true,
		DefaultVisibleRoleIDs:     model.UUIDList{},
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("插入测试频道失败: %v", err)
	}
	return token, username, channel.ID
}

// TestClientTextFlow 用户端文本频道全链路：
// 发消息（author_username）→ presign/上传/绑定附件（URL 前缀安全）→ 无鉴权签名下载 →
// 编辑与编辑历史 → 反应 → 搜索 → typing（事件发布 + 非成员 404）。
func TestClientTextFlow(t *testing.T) {
	router, db, bus := newTextRouter(t)
	token, username, channelID := setupTextFixture(t, router, db)
	base := "/gapi/v1/channels/" + channelID.String()

	// 订阅事件总线，捕获 TYPING_START。
	var mu sync.Mutex
	typingCount := 0
	bus.Subscribe(func(event eventbus.Event) {
		if event.Type == eventbus.EventTypingStart {
			mu.Lock()
			typingCount++
			mu.Unlock()
		}
	})

	// 1. 发送纯文本消息，响应带 author_username。
	rec, msg := doJSONReq(t, router, http.MethodPost, base+"/messages", token, map[string]string{"content": "你好 integration hello"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("发消息返回 %d: %s", rec.Code, rec.Body.String())
	}
	if msg["author_username"] != username {
		t.Errorf("author_username=%v，期待 %s", msg["author_username"], username)
	}
	messageID := msg["id"].(string)

	// 2. presign：upload_url 必须为 /gapi/v1 前缀且不含 /api/v1。
	payload := []byte("attachment-bytes-for-integration")
	rec, presign := doJSONReq(t, router, http.MethodPost, base+"/attachments/presign", token, map[string]any{
		"filename": "hello.txt", "size": len(payload), "mime": "text/plain",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("presign 返回 %d: %s", rec.Code, rec.Body.String())
	}
	uploadURL := presign["upload_url"].(string)
	if !strings.HasPrefix(uploadURL, "/gapi/v1/attachments/") || strings.Contains(uploadURL, "/api/v1") {
		t.Fatalf("用户端 upload_url 前缀不安全: %s", uploadURL)
	}

	// 3. 按 presign 返回的 URL 直传内容。
	rec, _ = doReq(t, router, http.MethodPut, uploadURL, token, payload, "application/octet-stream")
	if rec.Code != http.StatusOK {
		t.Fatalf("上传附件返回 %d: %s", rec.Code, rec.Body.String())
	}

	// 4. 发送带附件消息：download_url 必须为 /gapi/v1 前缀且不含 /api/v1。
	rec, withAttachment := doJSONReq(t, router, http.MethodPost, base+"/messages", token, map[string]any{
		"content": "带附件", "attachment_ids": []string{presign["attachment_id"].(string)},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("发附件消息返回 %d: %s", rec.Code, rec.Body.String())
	}
	attachments := withAttachment["attachments"].([]any)
	if len(attachments) != 1 {
		t.Fatalf("附件数量 %d，期待 1", len(attachments))
	}
	downloadURL := attachments[0].(map[string]any)["download_url"].(string)
	if !strings.HasPrefix(downloadURL, "/gapi/v1/attachments/") || strings.Contains(downloadURL, "/api/v1") {
		t.Fatalf("用户端 download_url 前缀不安全: %s", downloadURL)
	}

	// 5. 签名下载无需登录态。
	rec, _ = doReq(t, router, http.MethodGet, downloadURL, "", nil, "")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("无鉴权签名下载失败（%d，%d 字节）", rec.Code, rec.Body.Len())
	}

	// 6. 编辑消息与编辑历史。
	rec, _ = doJSONReq(t, router, http.MethodPatch, base+"/messages/"+messageID, token, map[string]string{"content": "编辑后 integration hello"})
	if rec.Code != http.StatusOK {
		t.Fatalf("编辑消息返回 %d: %s", rec.Code, rec.Body.String())
	}
	rec, edits := doJSONReq(t, router, http.MethodGet, base+"/messages/"+messageID+"/edits", token, nil)
	if rec.Code != http.StatusOK || len(edits["edits"].([]any)) != 1 {
		t.Fatalf("编辑历史返回 %d: %s", rec.Code, rec.Body.String())
	}

	// 7. 表情反应幂等 PUT/DELETE，且列表/单条拉取应带回 reactions（刷新后客户端可恢复）。
	rec, _ = doJSONReq(t, router, http.MethodPut, base+"/messages/"+messageID+"/reactions/👍/@me", token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("添加反应返回 %d: %s", rec.Code, rec.Body.String())
	}
	// 编码路径（与桌面端 encodeURIComponent 一致）也应幂等成功。
	rec, _ = doJSONReq(t, router, http.MethodPut, base+"/messages/"+messageID+"/reactions/"+url.PathEscape("👍")+"/@me", token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("编码路径再次添加反应返回 %d: %s", rec.Code, rec.Body.String())
	}
	rec, listedAfterReact := doJSONReq(t, router, http.MethodGet, base+"/messages", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("添加反应后拉列表返回 %d: %s", rec.Code, rec.Body.String())
	}
	foundReaction := false
	for _, item := range listedAfterReact["messages"].([]any) {
		msg := item.(map[string]any)
		if msg["id"] != messageID {
			continue
		}
		reactions, _ := msg["reactions"].([]any)
		for _, raw := range reactions {
			r := raw.(map[string]any)
			if r["emoji"] == "👍" && r["count"].(float64) == 1 && r["me"] == true {
				foundReaction = true
				break
			}
		}
	}
	if !foundReaction {
		t.Fatalf("消息列表未带回反应聚合: %s", rec.Body.String())
	}
	rec, oneAfterReact := doJSONReq(t, router, http.MethodGet, base+"/messages/"+messageID, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("添加反应后拉单条返回 %d: %s", rec.Code, rec.Body.String())
	}
	if reactions, _ := oneAfterReact["reactions"].([]any); len(reactions) == 0 {
		t.Fatalf("单条消息未带回反应: %s", rec.Body.String())
	}
	rec, _ = doJSONReq(t, router, http.MethodDelete, base+"/messages/"+messageID+"/reactions/👍/@me", token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("移除反应返回 %d: %s", rec.Code, rec.Body.String())
	}

	// 8. 搜索（ILIKE 兜底保证索引异步期内即刻可命中）。
	rec, search := doJSONReq(t, router, http.MethodGet, "/gapi/v1/search/messages?q=integration", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("搜索返回 %d: %s", rec.Code, rec.Body.String())
	}
	if hits := search["messages"].([]any); len(hits) == 0 {
		t.Error("搜索未命中刚发送的消息")
	}

	// 9. 消息列表带 author_username（批量联查）。
	rec, list := doJSONReq(t, router, http.MethodGet, base+"/messages", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("拉取消息返回 %d: %s", rec.Code, rec.Body.String())
	}
	for _, item := range list["messages"].([]any) {
		if item.(map[string]any)["author_username"] != username {
			t.Fatalf("消息列表 author_username 缺失: %v", item)
		}
	}

	// 10. typing：成员 204 并发布 TYPING_START；非成员 404（防扫频）。
	rec, _ = doJSONReq(t, router, http.MethodPost, base+"/typing", token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("typing 返回 %d: %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		count := typingCount
		mu.Unlock()
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("未收到 TYPING_START 事件（count=%d）", count)
		}
		time.Sleep(10 * time.Millisecond)
	}

	strangerName := "msg_s" + fmt.Sprintf("%07x", rand.Uint32())
	rec, stranger := doJSONReq(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", map[string]string{
		"username": strangerName, "email": strangerName + "@test.local", "password": "password123",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("注册路人返回 %d: %s", rec.Code, rec.Body.String())
	}
	rec, _ = doJSONReq(t, router, http.MethodPost, base+"/typing", stranger["access_token"].(string), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非成员 typing 返回 %d，期待 404", rec.Code)
	}

	// 11. 管理端点不存在于用户端前缀（成员带合法 token 也 404）。
	rec, _ = doJSONReq(t, router, http.MethodPatch, "/gapi/v1/guilds/"+uuid.NewString()+"/message-retention", token, map[string]int{"retention_days": 1})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("用户端保留策略管理端点返回 %d，期待 404（不挂载）", rec.Code)
	}
}

// TestClientSlowmodeAppliesToOwnerAndRoleExemption 慢速模式默认对所有成员生效，
// 包括拥有全部管理权限的服主；显式配置的角色才可豁免。
func TestClientSlowmodeAppliesToOwnerAndRoleExemption(t *testing.T) {
	router, db, _ := newTextRouter(t)
	token, _, channelID := setupTextFixture(t, router, db)
	if err := db.Model(&model.Channel{}).Where("id = ?", channelID).
		Update("rate_limit_per_user", 60).Error; err != nil {
		t.Fatalf("设置慢速模式失败: %v", err)
	}
	base := "/gapi/v1/channels/" + channelID.String() + "/messages"
	rec, _ := doJSONReq(t, router, http.MethodPost, base, token, map[string]string{"content": "第一条"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("首条消息返回 %d: %s", rec.Code, rec.Body.String())
	}
	rec, body := doJSONReq(t, router, http.MethodPost, base, token, map[string]string{"content": "第二条"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("服主未受慢速模式限制，返回 %d: %s", rec.Code, rec.Body.String())
	}
	errorBody, _ := body["error"].(map[string]any)
	if errorBody["code"] != "SLOWMODE_RATE_LIMITED" {
		t.Fatalf("慢速模式错误码异常: %s", rec.Body.String())
	}

	var channel model.Channel
	if err := db.First(&channel, "id = ?", channelID).Error; err != nil {
		t.Fatalf("读取频道失败: %v", err)
	}
	var everyone model.Role
	if err := db.First(&everyone, "guild_id = ? AND is_everyone = true", channel.GuildID).Error; err != nil {
		t.Fatalf("读取 @everyone 角色失败: %v", err)
	}
	if err := db.Model(&model.Channel{}).Where("id = ?", channelID).
		Update("rate_limit_exempt_role_ids", model.UUIDList{everyone.ID}).Error; err != nil {
		t.Fatalf("配置豁免角色失败: %v", err)
	}
	rec, _ = doJSONReq(t, router, http.MethodPost, base, token, map[string]string{"content": "豁免后发送"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("@everyone 豁免后返回 %d: %s", rec.Code, rec.Body.String())
	}
}
