package message_test

// 提及解析与未读同步（docs 05 FR-19/FR-22、docs 15）集成测试：需要真实 PostgreSQL。
//
// 运行方式（默认跳过，不影响 go test ./...）：
//
//	TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/owl_test?sslmode=disable' go test ./internal/message/
//
// 覆盖：wire format 解析与成员/角色校验、@everyone 权限门控、角色提及展开、
// 可见性过滤（禁看不计数）、编辑重解析、ack 单调推进与清零、READ_STATE_UPDATE
// 跨端定向事件、GET /users/@me/read-states 兜底、READY read_states 的 DB 组装路径。

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
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"github.com/owlspeak/owl-server/backend/internal/snapshot"
	"gorm.io/gorm"
)

// signupAndJoin 注册新用户并直接落库加入 guild，返回 token 与用户/成员 ID。
func signupAndJoin(t *testing.T, router *gin.Engine, db *gorm.DB, guildID uuid.UUID, prefix string) (token string, userID, memberID uuid.UUID) {
	t.Helper()
	username := prefix + fmt.Sprintf("%08x", rand.Uint32())
	rec, body := doJSONReq(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", map[string]string{
		"username": username, "email": username + "@test.local", "password": "password123",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("注册 %s 返回 %d: %s", username, rec.Code, rec.Body.String())
	}
	token = body["access_token"].(string)
	var user model.User
	if err := db.First(&user, "username = ?", username).Error; err != nil {
		t.Fatalf("查找用户 %s 失败: %v", username, err)
	}
	member := model.Member{ID: uuid.New(), GuildID: guildID, UserID: user.ID}
	if err := db.Create(&member).Error; err != nil {
		t.Fatalf("插入成员失败: %v", err)
	}
	return token, user.ID, member.ID
}

// readStateRow 查询某用户在某频道的 read state 行；无记录返回 ok=false。
func readStateRow(t *testing.T, db *gorm.DB, userID, channelID uuid.UUID) (model.ReadState, bool) {
	t.Helper()
	var state model.ReadState
	err := db.First(&state, "user_id = ? AND channel_id = ?", userID, channelID).Error
	if err == gorm.ErrRecordNotFound {
		return state, false
	}
	if err != nil {
		t.Fatalf("查询 read state 失败: %v", err)
	}
	return state, true
}

func mentionStrings(t *testing.T, payload map[string]any, key string) []string {
	t.Helper()
	raw, ok := payload[key].([]any)
	if !ok {
		t.Fatalf("%s 字段缺失或非数组: %v", key, payload[key])
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		result = append(result, item.(string))
	}
	return result
}

// TestMentionsAndReadStates 提及 + 未读全链路（场景见文件头注释）。
func TestMentionsAndReadStates(t *testing.T) {
	router, db, bus := newTextRouter(t)
	ownerToken, ownerName, channelID := setupTextFixture(t, router, db)
	base := "/gapi/v1/channels/" + channelID.String()

	var channel model.Channel
	if err := db.First(&channel, "id = ?", channelID).Error; err != nil {
		t.Fatalf("查询频道失败: %v", err)
	}
	guildID := channel.GuildID
	var owner model.User
	if err := db.First(&owner, "username = ?", ownerName).Error; err != nil {
		t.Fatalf("查询服主失败: %v", err)
	}

	tokenB, userB, _ := signupAndJoin(t, router, db, guildID, "rs_b")
	_, userC, memberC := signupAndJoin(t, router, db, guildID, "rs_c")

	role := model.Role{ID: uuid.New(), GuildID: guildID, Name: "raiders-" + fmt.Sprintf("%06x", rand.Uint32())}
	if err := db.Create(&role).Error; err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	if err := db.Create(&model.MemberRole{MemberID: memberC, RoleID: role.ID}).Error; err != nil {
		t.Fatalf("绑定角色失败: %v", err)
	}

	// 捕获 READ_STATE_UPDATE 定向事件。
	var mu sync.Mutex
	var readStateEvents []eventbus.Event
	bus.Subscribe(func(event eventbus.Event) {
		if event.Type == eventbus.EventReadStateUpdate {
			mu.Lock()
			readStateEvents = append(readStateEvents, event)
			mu.Unlock()
		}
	})

	// 1. 服主发消息：用户提及（含非成员）+ 角色提及（含不存在的角色）→
	//    只有成员/本服角色通过校验；B 直接计数，C 经角色展开计数，作者不计。
	nonMember := uuid.New()
	fakeRole := uuid.New()
	content1 := "开黑 <@" + userB.String() + "> <@&" + role.ID.String() + "> <@" + nonMember.String() + "> <@&" + fakeRole.String() + ">"
	rec, msg1 := doJSONReq(t, router, http.MethodPost, base+"/messages", ownerToken, map[string]string{"content": content1})
	if rec.Code != http.StatusCreated {
		t.Fatalf("发消息返回 %d: %s", rec.Code, rec.Body.String())
	}
	if got := mentionStrings(t, msg1, "mentions"); len(got) != 1 || got[0] != userB.String() {
		t.Errorf("mentions = %v，期待仅 [%s]（非成员应被过滤）", got, userB)
	}
	if got := mentionStrings(t, msg1, "mention_roles"); len(got) != 1 || got[0] != role.ID.String() {
		t.Errorf("mention_roles = %v，期待仅 [%s]（非本服角色应被过滤）", got, role.ID)
	}
	if msg1["mention_everyone"] != false {
		t.Errorf("mention_everyone = %v，期待 false", msg1["mention_everyone"])
	}
	msg1ID := msg1["id"].(string)
	if state, ok := readStateRow(t, db, userB, channelID); !ok || state.MentionCount != 1 {
		t.Errorf("B 的 mention_count = %+v，期待 1（直接提及）", state)
	}
	if state, ok := readStateRow(t, db, userC, channelID); !ok || state.MentionCount != 1 {
		t.Errorf("C 的 mention_count = %+v，期待 1（角色展开）", state)
	}
	if _, ok := readStateRow(t, db, owner.ID, channelID); ok {
		t.Error("作者自己不应产生 mention_count 记录")
	}

	// 2. 无 MENTION_EVERYONE 权限的成员发 @everyone：字面量不生效、无人计数、正文保留。
	rec, msgB := doJSONReq(t, router, http.MethodPost, base+"/messages", tokenB, map[string]string{"content": "@everyone 看我"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("B 发消息返回 %d: %s", rec.Code, rec.Body.String())
	}
	if msgB["mention_everyone"] != false {
		t.Errorf("无权限成员的 mention_everyone = %v，期待 false", msgB["mention_everyone"])
	}
	if msgB["content"] != "@everyone 看我" {
		t.Errorf("正文应原样保留: %v", msgB["content"])
	}
	if state, _ := readStateRow(t, db, userC, channelID); state.MentionCount != 1 {
		t.Errorf("无权限 @everyone 后 C 的 mention_count = %d，期待仍为 1", state.MentionCount)
	}

	// 3. 服主（有 MENTION_EVERYONE）发 @everyone：生效并展开到频道可见成员（作者除外）。
	rec, msg3 := doJSONReq(t, router, http.MethodPost, base+"/messages", ownerToken, map[string]string{"content": "@everyone 全员集合"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("发 @everyone 返回 %d: %s", rec.Code, rec.Body.String())
	}
	if msg3["mention_everyone"] != true {
		t.Errorf("服主 mention_everyone = %v，期待 true", msg3["mention_everyone"])
	}
	msg3ID := msg3["id"].(string)
	if state, _ := readStateRow(t, db, userB, channelID); state.MentionCount != 2 {
		t.Errorf("@everyone 后 B 的 mention_count = %d，期待 2", state.MentionCount)
	}
	if state, _ := readStateRow(t, db, userC, channelID); state.MentionCount != 2 {
		t.Errorf("@everyone 后 C 的 mention_count = %d，期待 2", state.MentionCount)
	}
	if _, ok := readStateRow(t, db, owner.ID, channelID); ok {
		t.Error("@everyone 也不应给作者自己计数")
	}

	// 4. 可见性过滤：B 被成员覆盖禁看后再被直接提及 → 不计数；C 正常 +1。
	overwrite := model.ChannelOverwrite{
		ID: uuid.New(), ChannelID: channelID, Type: model.OverwriteMember,
		TargetID: userB, Deny: int64(rbac.ViewChannel),
	}
	if err := db.Create(&overwrite).Error; err != nil {
		t.Fatalf("创建覆盖失败: %v", err)
	}
	rec, msg4 := doJSONReq(t, router, http.MethodPost, base+"/messages", ownerToken, map[string]string{
		"content": "悄悄说 <@" + userB.String() + "> <@" + userC.String() + ">",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("发消息返回 %d: %s", rec.Code, rec.Body.String())
	}
	if state, _ := readStateRow(t, db, userB, channelID); state.MentionCount != 2 {
		t.Errorf("禁看期间 B 的 mention_count = %d，期待仍为 2（不可见不计数）", state.MentionCount)
	}
	if state, _ := readStateRow(t, db, userC, channelID); state.MentionCount != 3 {
		t.Errorf("C 的 mention_count = %d，期待 3", state.MentionCount)
	}
	if err := db.Delete(&overwrite).Error; err != nil {
		t.Fatalf("删除覆盖失败: %v", err)
	}

	// 5. 编辑重解析：改掉正文提及后 mentions/mention_roles 同步更新。
	msg4ID := msg4["id"].(string)
	rec, edited := doJSONReq(t, router, http.MethodPatch, base+"/messages/"+msg4ID, ownerToken, map[string]string{
		"content": "改口只提 <@" + userC.String() + ">",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("编辑返回 %d: %s", rec.Code, rec.Body.String())
	}
	if got := mentionStrings(t, edited, "mentions"); len(got) != 1 || got[0] != userC.String() {
		t.Errorf("编辑后 mentions = %v，期待仅 [%s]", got, userC)
	}

	// 6. ack：推进到 msg3 → mention_count 清零 + READ_STATE_UPDATE 定向事件；
	//    再 ack 更旧的 msg1 → last_read 不后退（单调性）。
	rec, acked := doJSONReq(t, router, http.MethodPost, base+"/messages/"+msg3ID+"/ack", tokenB, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("ack 返回 %d: %s", rec.Code, rec.Body.String())
	}
	if acked["last_read_message_id"] != msg3ID || acked["mention_count"] != float64(0) {
		t.Errorf("ack 响应异常: %v", acked)
	}
	if state, _ := readStateRow(t, db, userB, channelID); state.MentionCount != 0 {
		t.Errorf("ack 后 B 的 mention_count = %d，期待 0", state.MentionCount)
	}
	rec, acked = doJSONReq(t, router, http.MethodPost, base+"/messages/"+msg1ID+"/ack", tokenB, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("回退 ack 返回 %d: %s", rec.Code, rec.Body.String())
	}
	if acked["last_read_message_id"] != msg3ID {
		t.Errorf("ack 旧消息后 last_read = %v，期待保持 %s（只前进不后退）", acked["last_read_message_id"], msg3ID)
	}

	// READ_STATE_UPDATE 事件：定向本人（UserIDs=[B]），payload 含频道与读位置。
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		count := len(readStateEvents)
		mu.Unlock()
		if count >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("未收到 READ_STATE_UPDATE 事件（count=%d）", count)
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	firstEvent := readStateEvents[0]
	mu.Unlock()
	if len(firstEvent.UserIDs) != 1 || firstEvent.UserIDs[0] != userB {
		t.Errorf("READ_STATE_UPDATE 应定向本人全部端: %v", firstEvent.UserIDs)
	}
	rawPayload, err := json.Marshal(firstEvent.Payload)
	if err != nil {
		t.Fatalf("序列化事件载荷失败: %v", err)
	}
	var payload struct {
		ChannelID         uuid.UUID `json:"channel_id"`
		LastReadMessageID string    `json:"last_read_message_id"`
		MentionCount      int       `json:"mention_count"`
	}
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		t.Fatalf("解析事件载荷失败: %v", err)
	}
	if payload.ChannelID != channelID || payload.LastReadMessageID != msg3ID || payload.MentionCount != 0 {
		t.Errorf("READ_STATE_UPDATE 载荷异常: %s", rawPayload)
	}

	// 7. ack 后再次被提及：计数从 0 重新累计。
	rec, _ = doJSONReq(t, router, http.MethodPost, base+"/messages", ownerToken, map[string]string{
		"content": "又来 <@" + userB.String() + ">",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("发消息返回 %d: %s", rec.Code, rec.Body.String())
	}
	if state, _ := readStateRow(t, db, userB, channelID); state.MentionCount != 1 {
		t.Errorf("ack 后再次提及 B 的 mention_count = %d，期待 1", state.MentionCount)
	}

	// 8. GET /users/@me/read-states REST 兜底（可选 guild_id 过滤）。
	rec, listing := doJSONReq(t, router, http.MethodGet, "/gapi/v1/users/@me/read-states", tokenB, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read-states 返回 %d: %s", rec.Code, rec.Body.String())
	}
	if states := listing["read_states"].([]any); len(states) != 1 {
		t.Fatalf("read_states 数量 = %d，期待 1", len(states))
	}
	rec, listing = doJSONReq(t, router, http.MethodGet, "/gapi/v1/users/@me/read-states?guild_id="+guildID.String(), tokenB, nil)
	if rec.Code != http.StatusOK || len(listing["read_states"].([]any)) != 1 {
		t.Fatalf("按 guild 过滤 read-states 异常: %d %s", rec.Code, rec.Body.String())
	}
	rec, listing = doJSONReq(t, router, http.MethodGet, "/gapi/v1/users/@me/read-states?guild_id="+uuid.NewString(), tokenB, nil)
	if rec.Code != http.StatusOK || len(listing["read_states"].([]any)) != 0 {
		t.Fatalf("无关 guild 过滤应为空: %d %s", rec.Code, rec.Body.String())
	}

	// 9. READY read_states 的 DB 组装路径（snapshot.BuildReadStates）。
	states, err := snapshot.BuildReadStates(db, userB, []uuid.UUID{channelID})
	if err != nil {
		t.Fatalf("BuildReadStates 失败: %v", err)
	}
	if len(states) != 1 || states[0].ChannelID != channelID || states[0].MentionCount != 1 {
		t.Fatalf("BuildReadStates = %+v，期待 1 条且 mention_count=1", states)
	}
	if states[0].LastReadMessageID == 0 {
		t.Error("BuildReadStates last_read_message_id 应为已 ack 的读位置")
	}
	empty, err := snapshot.BuildReadStates(db, userB, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空频道集合应返回空数组: %v %v", empty, err)
	}

	// 10. 频道不可见者调用 ack 一律 404（防扫频）。
	strangerToken, _, _ := func() (string, uuid.UUID, uuid.UUID) {
		username := "rs_x" + fmt.Sprintf("%07x", rand.Uint32())
		rec, body := doJSONReq(t, router, http.MethodPost, "/gapi/v1/auth/signup", "", map[string]string{
			"username": username, "email": username + "@test.local", "password": "password123",
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("注册路人返回 %d", rec.Code)
		}
		return body["access_token"].(string), uuid.Nil, uuid.Nil
	}()
	rec, _ = doJSONReq(t, router, http.MethodPost, base+"/messages/"+msg3ID+"/ack", strangerToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非成员 ack 返回 %d，期待 404", rec.Code)
	}
}
