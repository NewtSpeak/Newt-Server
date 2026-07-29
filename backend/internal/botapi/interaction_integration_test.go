package botapi_test

// bot 交互按钮 + ephemeral 消息全链路集成测试（设计文档 2026-07-26）：需要真实 PostgreSQL。
//
//	TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/owl_test?sslmode=disable' go test ./internal/botapi/
//
// 覆盖：差异化按钮双视角裁剪 → 点隐藏按钮 404 / disabled 400 → 点击 202 →
// INTERACTION_CREATE 定向 bot（含 token）→ callback ack/reply(ephemeral)/update_message →
// 重复回应 409 → ephemeral 历史过滤 / 单条 404 / 禁回复 / 禁反应 → 非 bot 发 visible_to 403。

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/security"
	"gorm.io/gorm"
)

// setupMemberUser 落库创建普通用户并加入服，返回其后台平面 Bearer 认证头。
// 普通用户不能走 /api/v1/auth/login（仅 system_admin）；测试直接签发 aud=admin
// token——后台消息平面鉴权只校验受众，不强制 system_admin。
func setupMemberUser(t *testing.T, router *gin.Engine, db *gorm.DB, guildID uuid.UUID, name string) (model.User, string) {
	t.Helper()
	_ = router
	suffix := fmt.Sprintf("%08x", rand.Uint32())
	hash, err := security.HashPassword("password123")
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	user := model.User{
		ID: uuid.New(), Username: name + "_" + suffix,
		Email: name + "_" + suffix + "@test.local", PasswordHash: hash,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := db.Create(&model.Member{ID: uuid.New(), GuildID: guildID, UserID: user.ID}).Error; err != nil {
		t.Fatalf("加入服失败: %v", err)
	}
	// 与 newBotRouter 中 JWTSecret / AccessTokenTTL 保持一致。
	tokens := security.NewTokenManager("integration-secret-integration-32", time.Minute)
	access, _, err := tokens.AccessTokenWithAudience(user.ID, security.AudienceAdmin)
	if err != nil {
		t.Fatalf("签发成员 access token 失败: %v", err)
	}
	return user, "Bearer " + access
}

func buttonsOf(t *testing.T, msg map[string]any) []any {
	t.Helper()
	card, ok := msg["card"].(map[string]any)
	if !ok {
		return nil
	}
	buttons, _ := card["buttons"].([]any)
	return buttons
}

func TestInteractionAndEphemeralFlow(t *testing.T) {
	router, db, bus := newBotRouter(t)
	adminAuth := setupAdmin(t, router, db)
	suffix := fmt.Sprintf("%08x", rand.Uint32())

	// 事件捕获：INTERACTION_CREATE（拿 token）与 INTERACTION_ACK / MESSAGE_* 定向计数。
	var mu sync.Mutex
	var interactionCreates []eventbus.Event
	var interactionAcks []eventbus.Event
	bus.Subscribe(func(event eventbus.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch event.Type {
		case eventbus.EventInteractionCreate:
			interactionCreates = append(interactionCreates, event)
		case eventbus.EventInteractionAck:
			interactionAcks = append(interactionAcks, event)
		}
	})

	// ---- 基建：bot + token + 服 + 频道 + 安装 + 两个普通成员（A 持角色 R，B 无）----
	rec, bot := doBotReq(t, router, http.MethodPost, "/api/v1/bots", adminAuth, map[string]string{
		"name": "交互测试机器人 " + suffix, "username": "ixbot_" + suffix,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建 bot 返回 %d: %s", rec.Code, rec.Body.String())
	}
	botID := bot["id"].(string)
	botUserID := bot["user_id"].(string)
	rec, issued := doBotReq(t, router, http.MethodPost, "/api/v1/bots/"+botID+"/tokens", adminAuth, map[string]string{"name": "ci"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("签发 token 返回 %d", rec.Code)
	}
	botAuth := "Bot " + issued["plain"].(string)

	rec, guild := doBotReq(t, router, http.MethodPost, "/api/v1/guilds", adminAuth, map[string]string{"name": "交互集成服 " + suffix})
	if rec.Code != http.StatusCreated {
		t.Fatalf("建服返回 %d", rec.Code)
	}
	guildID := uuid.MustParse(guild["id"].(string))
	channel := model.Channel{ID: uuid.New(), GuildID: guildID, Name: "general", Type: model.ChannelText}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("插入频道失败: %v", err)
	}
	rec, _ = doBotReq(t, router, http.MethodPut, "/api/v1/guilds/"+guildID.String()+"/bots/"+botID, adminAuth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("安装 bot 返回 %d", rec.Code)
	}

	userA, authA := setupMemberUser(t, router, db, guildID, "alice")
	userB, authB := setupMemberUser(t, router, db, guildID, "bob")
	roleR := model.Role{ID: uuid.New(), GuildID: guildID, Name: "审批员" + suffix, Permissions: 0, Position: 1}
	if err := db.Create(&roleR).Error; err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	var memberA model.Member
	if err := db.First(&memberA, "guild_id = ? AND user_id = ?", guildID, userA.ID).Error; err != nil {
		t.Fatalf("读取成员失败: %v", err)
	}
	if err := db.Create(&model.MemberRole{MemberID: memberA.ID, RoleID: roleR.ID}).Error; err != nil {
		t.Fatalf("绑定角色失败: %v", err)
	}

	botBase := "/bot-api/v1/channels/" + channel.ID.String()
	userBase := "/api/v1/channels/" + channel.ID.String()

	// ---- 1. bot 发差异化按钮卡片 ----
	card := map[string]any{
		"title": "部署审批",
		"buttons": []map[string]any{
			{"label": "查看日志", "url": "https://ci.example.com/log/42"},
			{"label": "批准", "custom_id": "deploy:approve", "style": "success", "size": "md"},
			{"label": "拒绝", "custom_id": "deploy:reject", "style": "danger", "disabled": true},
			{"label": "审批员专属", "custom_id": "deploy:secret", "visible_to": map[string]any{"roles": []string{roleR.ID.String()}}},
		},
	}
	rec, msg := doBotReq(t, router, http.MethodPost, botBase+"/messages", botAuth, map[string]any{"card": card})
	if rec.Code != http.StatusCreated {
		t.Fatalf("bot 发卡片返回 %d: %s", rec.Code, rec.Body.String())
	}
	messageID := msg["id"].(string)
	if got := len(buttonsOf(t, msg)); got != 4 {
		t.Errorf("作者响应应含全量 4 按钮，得到 %d", got)
	}

	// ---- 2. 双视角裁剪：A 见 4 个（含角色按钮），B 见 3 个，均无 visible_to ----
	rec, msgForA := doBotReq(t, router, http.MethodGet, userBase+"/messages/"+messageID, authA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("A 读消息返回 %d: %s", rec.Code, rec.Body.String())
	}
	if got := len(buttonsOf(t, msgForA)); got != 4 {
		t.Errorf("A（持角色）应见 4 按钮，得到 %d", got)
	}
	rec, msgForB := doBotReq(t, router, http.MethodGet, userBase+"/messages/"+messageID, authB, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("B 读消息返回 %d", rec.Code)
	}
	if got := len(buttonsOf(t, msgForB)); got != 3 {
		t.Errorf("B（无角色）应见 3 按钮，得到 %d", got)
	}
	for _, raw := range append(buttonsOf(t, msgForA), buttonsOf(t, msgForB)...) {
		if _, leaked := raw.(map[string]any)["visible_to"]; leaked {
			t.Errorf("下发按钮不应含 visible_to：%v", raw)
		}
	}

	// ---- 3. 负例：B 点隐藏按钮 404；disabled 按钮 400；不存在的 custom_id 404 ----
	rec, _ = doBotReq(t, router, http.MethodPost, userBase+"/messages/"+messageID+"/interactions", authB, map[string]string{"custom_id": "deploy:secret"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("点隐藏按钮应 404，实际 %d", rec.Code)
	}
	rec, _ = doBotReq(t, router, http.MethodPost, userBase+"/messages/"+messageID+"/interactions", authA, map[string]string{"custom_id": "deploy:reject"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("点 disabled 按钮应 400，实际 %d", rec.Code)
	}
	rec, _ = doBotReq(t, router, http.MethodPost, userBase+"/messages/"+messageID+"/interactions", authA, map[string]string{"custom_id": "nope"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("不存在的 custom_id 应 404，实际 %d", rec.Code)
	}

	// ---- 4. B 点公开按钮：202 + INTERACTION_CREATE 定向 bot ----
	rec, clicked := doBotReq(t, router, http.MethodPost, userBase+"/messages/"+messageID+"/interactions", authB, map[string]string{"custom_id": "deploy:approve"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("点击返回 %d: %s", rec.Code, rec.Body.String())
	}
	interactionID := clicked["interaction_id"].(string)
	mu.Lock()
	if len(interactionCreates) != 1 {
		mu.Unlock()
		t.Fatalf("应产生 1 条 INTERACTION_CREATE，得到 %d", len(interactionCreates))
	}
	event := interactionCreates[0]
	mu.Unlock()
	if len(event.UserIDs) != 1 || event.UserIDs[0].String() != botUserID {
		t.Errorf("INTERACTION_CREATE 应定向 bot 用户：%v", event.UserIDs)
	}
	// 从事件载荷取 token（载荷为 message 包内私有结构体，经 JSON round-trip 取值）。
	token := extractField(t, event.Payload, "token")
	if token == "" {
		t.Fatal("INTERACTION_CREATE 载荷缺少 token")
	}

	// ---- 5. bot ack → ACKED，INTERACTION_ACK 定向点击者 ----
	rec, _ = doBotReq(t, router, http.MethodPost, "/bot-api/v1/interactions/"+interactionID+"/callback", botAuth, map[string]any{
		"token": token, "type": "ack",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("ack 返回 %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	if len(interactionAcks) == 0 || interactionAcks[len(interactionAcks)-1].UserIDs[0] != userB.ID {
		t.Errorf("INTERACTION_ACK 应定向点击者 B")
	}
	mu.Unlock()

	// 错 token 404。
	rec, _ = doBotReq(t, router, http.MethodPost, "/bot-api/v1/interactions/"+interactionID+"/callback", botAuth, map[string]any{
		"token": "owlint_wrong", "type": "ack",
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("错误 token 应 404，实际 %d", rec.Code)
	}

	// ---- 6. bot reply（默认 ephemeral）：仅 B 可见 ----
	rec, reply := doBotReq(t, router, http.MethodPost, "/bot-api/v1/interactions/"+interactionID+"/callback", botAuth, map[string]any{
		"token": token, "type": "reply", "content": "已收到你的批准",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("reply 返回 %d: %s", rec.Code, rec.Body.String())
	}
	replyID := reply["id"].(string)
	visibleTo, _ := reply["visible_to"].([]any)
	if len(visibleTo) != 1 || visibleTo[0].(string) != userB.ID.String() {
		t.Errorf("ephemeral reply visible_to 应为点击者：%v", reply["visible_to"])
	}
	// B 可读、A 404。
	rec, _ = doBotReq(t, router, http.MethodGet, userBase+"/messages/"+replyID, authB, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("B 读 ephemeral 回复应 200，实际 %d", rec.Code)
	}
	rec, _ = doBotReq(t, router, http.MethodGet, userBase+"/messages/"+replyID, authA, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("A 读 ephemeral 回复应 404，实际 %d", rec.Code)
	}
	// 历史过滤：A 的列表不含、B 的列表含。
	rec, listA := doBotReq(t, router, http.MethodGet, userBase+"/messages", authA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("A 拉历史返回 %d", rec.Code)
	}
	rec, listB := doBotReq(t, router, http.MethodGet, userBase+"/messages", authB, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("B 拉历史返回 %d", rec.Code)
	}
	if containsMessage(listA, replyID) {
		t.Error("A 的历史不应含 ephemeral 回复")
	}
	if !containsMessage(listB, replyID) {
		t.Error("B 的历史应含 ephemeral 回复")
	}

	// ---- 7. 重复回应 409 ----
	rec, _ = doBotReq(t, router, http.MethodPost, "/bot-api/v1/interactions/"+interactionID+"/callback", botAuth, map[string]any{
		"token": token, "type": "reply", "content": "再来一次",
	})
	if rec.Code != http.StatusConflict {
		t.Errorf("重复回应应 409，实际 %d", rec.Code)
	}

	// ---- 8. A 点角色按钮 → update_message 置灰 ----
	rec, clickedA := doBotReq(t, router, http.MethodPost, userBase+"/messages/"+messageID+"/interactions", authA, map[string]string{"custom_id": "deploy:secret"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("A 点角色按钮返回 %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	tokenA := extractField(t, interactionCreates[len(interactionCreates)-1].Payload, "token")
	mu.Unlock()
	rec, updated := doBotReq(t, router, http.MethodPost, "/bot-api/v1/interactions/"+clickedA["interaction_id"].(string)+"/callback", botAuth, map[string]any{
		"token": tokenA, "type": "update_message",
		"card": map[string]any{"title": "已批准", "buttons": []map[string]any{
			{"label": "已处理", "custom_id": "noop", "disabled": true},
		}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update_message 返回 %d: %s", rec.Code, rec.Body.String())
	}
	if updated["card"].(map[string]any)["title"] != "已批准" {
		t.Errorf("update_message 后卡片未更新：%v", updated["card"])
	}

	// ---- 9. ephemeral 约束：非 bot 发 403；禁回复；禁反应 ----
	rec, _ = doBotReq(t, router, http.MethodPost, userBase+"/messages", authA, map[string]any{
		"content": "我也想发 ephemeral", "visible_to_user_ids": []string{userB.ID.String()},
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("非 bot 发 visible_to 应 403，实际 %d", rec.Code)
	}
	rec, _ = doBotReq(t, router, http.MethodPost, userBase+"/messages", authB, map[string]any{
		"content": "回复 ephemeral", "reply_to_id": replyID,
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("回复 ephemeral 应 400，实际 %d", rec.Code)
	}
	rec, _ = doBotReq(t, router, http.MethodPut, userBase+"/messages/"+replyID+"/reactions/%F0%9F%91%8D/@me", authB, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("对 ephemeral 加反应应 404，实际 %d", rec.Code)
	}
}

// extractField 经 JSON round-trip 从事件载荷取字符串字段（载荷为包内私有结构体）。
func extractField(t *testing.T, payload any, field string) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化事件载荷失败: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("反序列化事件载荷失败: %v", err)
	}
	value, _ := parsed[field].(string)
	return value
}

func containsMessage(list map[string]any, id string) bool {
	messages, _ := list["messages"].([]any)
	for _, raw := range messages {
		if msg, ok := raw.(map[string]any); ok && msg["id"] == id {
			return true
		}
	}
	return false
}
