package gateway

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/snapshot"
)

// ---- mock 实现：摆脱 PostgreSQL 依赖 ----

// stubAuth token → 用户的静态映射；未登记的 token 视为无效。
type stubAuth struct {
	users    map[string]model.User
	guildIDs []uuid.UUID
}

func (a *stubAuth) Authenticate(token string) (model.User, []uuid.UUID, error) {
	user, ok := a.users[token]
	if !ok {
		return model.User{}, nil, errors.New("无效令牌")
	}
	return user, a.guildIDs, nil
}

// stubDirectory 静态成员表 + 可配置的可见性判定与 READY 快照。
type stubDirectory struct {
	mu          sync.Mutex
	members     map[uuid.UUID][]uuid.UUID
	invisible   map[uuid.UUID]bool // userID → 对任意频道不可见
	snapshots   map[uuid.UUID]snapshot.Guild
	readStates  map[uuid.UUID][]snapshot.ReadState // userID → 全量 read state（按请求频道过滤）
	memberCalls int
}

func (d *stubDirectory) GuildMemberIDs(guildID uuid.UUID) ([]uuid.UUID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.memberCalls++
	return d.members[guildID], nil
}

func (d *stubDirectory) CanSeeChannel(user model.User, guildID, channelID uuid.UUID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.invisible[user.ID]
}

func (d *stubDirectory) CanAccessChannelContent(user model.User, guildID, channelID uuid.UUID) bool {
	return d.CanSeeChannel(user, guildID, channelID)
}

// ReadStates 与生产实现同语义：只返回落在给定可见频道集合内的记录。
func (d *stubDirectory) ReadStates(userID uuid.UUID, channelIDs []uuid.UUID) ([]snapshot.ReadState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	visible := make(map[uuid.UUID]struct{}, len(channelIDs))
	for _, id := range channelIDs {
		visible[id] = struct{}{}
	}
	result := []snapshot.ReadState{}
	for _, state := range d.readStates[userID] {
		if _, ok := visible[state.ChannelID]; ok {
			result = append(result, state)
		}
	}
	return result, nil
}

func (d *stubDirectory) GuildSnapshots(user model.User, guildIDs []uuid.UUID) ([]snapshot.Guild, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := make([]snapshot.Guild, 0, len(guildIDs))
	for _, guildID := range guildIDs {
		if snap, ok := d.snapshots[guildID]; ok {
			result = append(result, snap)
		}
	}
	return result, nil
}

func (d *stubDirectory) SocialSnapshot(userID uuid.UUID) (any, any, any, int64) {
	return []any{}, map[string]any{}, []any{}, int64(0)
}

// ---- 测试基础设施 ----

func testOptions() options {
	return options{
		HeartbeatInterval: 200 * time.Millisecond,
		IdentifyTimeout:   300 * time.Millisecond,
		WriteTimeout:      time.Second,
		SendBuffer:        16,
		ReplayBufferSize:  32,
		ReplayTTL:         time.Minute,
		ResumeWindow:      time.Minute,
		SweepInterval:     20 * time.Millisecond,
	}
}

// newTestServer 起一个只挂 /api/v1/gateway 的 httptest 服务。
func newTestServer(t *testing.T, h *handler) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/gateway", h.serve)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

// dial 建立 WS 连接（未握手）。
func dial(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/gateway"
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("连接 gateway 失败: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

// testFrame 下行帧的测试视图（含 DISPATCH 序列号 s）。
type testFrame struct {
	Op string          `json:"op"`
	T  string          `json:"t"`
	S  int64           `json:"s"`
	D  json.RawMessage `json:"d"`
}

// readFrame 带超时读取一帧并解析。
func readFrame(t *testing.T, ws *websocket.Conn) testFrame {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("读取帧失败: %v", err)
	}
	var f testFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("解析帧失败: %v (%s)", err, raw)
	}
	return f
}

// expectClose 期待连接以指定关闭码被服务端关闭。
func expectClose(t *testing.T, ws *websocket.Conn, wantCode int) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := ws.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("期待关闭错误，实际: %v", err)
	}
	if closeErr.Code != wantCode {
		t.Fatalf("关闭码 = %d，期待 %d（%s）", closeErr.Code, wantCode, closeErr.Text)
	}
}

// handshake 完成 HELLO → IDENTIFY → READY，返回已就绪的客户端连接与 session_id。
func handshake(t *testing.T, server *httptest.Server, token string) (*websocket.Conn, string) {
	t.Helper()
	ws := dial(t, server)
	if f := readFrame(t, ws); f.Op != opHello {
		t.Fatalf("首帧 = %s，期待 HELLO", f.Op)
	}
	sendFrame(t, ws, map[string]any{"op": opIdentify, "d": map[string]string{"token": token}})
	f := readFrame(t, ws)
	if f.Op != opReady {
		t.Fatalf("IDENTIFY 后收到 %s，期待 READY", f.Op)
	}
	var ready readyData
	if err := json.Unmarshal(f.D, &ready); err != nil {
		t.Fatalf("READY 载荷解析失败: %v", err)
	}
	if ready.SessionID == "" {
		t.Fatalf("READY 缺少 session_id: %s", f.D)
	}
	return ws, ready.SessionID
}

func sendFrame(t *testing.T, ws *websocket.Conn, frame any) {
	t.Helper()
	if err := ws.WriteJSON(frame); err != nil {
		t.Fatalf("发送帧失败: %v", err)
	}
}

// ---- 握手协议 ----

func TestHandshakeHelloIdentifyReadyHeartbeat(t *testing.T) {
	guildID := uuid.New()
	user := model.User{ID: uuid.New(), Username: "alice"}
	auth := &stubAuth{users: map[string]model.User{"good-token": user}, guildIDs: []uuid.UUID{guildID}}
	h := newHandler(auth, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	ws := dial(t, server)
	f := readFrame(t, ws)
	if f.Op != opHello {
		t.Fatalf("首帧 = %s，期待 HELLO", f.Op)
	}
	var hello helloData
	if err := json.Unmarshal(f.D, &hello); err != nil || hello.HeartbeatIntervalMS != testOptions().HeartbeatInterval.Milliseconds() {
		t.Fatalf("HELLO 载荷异常: %s", f.D)
	}

	sendFrame(t, ws, map[string]any{"op": opIdentify, "d": map[string]string{"token": "good-token"}})
	f = readFrame(t, ws)
	if f.Op != opReady {
		t.Fatalf("IDENTIFY 后收到 %s，期待 READY", f.Op)
	}
	var ready readyData
	if err := json.Unmarshal(f.D, &ready); err != nil {
		t.Fatalf("READY 载荷解析失败: %v", err)
	}
	if ready.SessionID == "" {
		t.Fatalf("READY 缺少 session_id")
	}
	if ready.User.ID != user.ID || ready.User.Username != "alice" {
		t.Fatalf("READY 用户信息异常: %+v", ready.User)
	}
	if len(ready.GuildIDs) != 1 || ready.GuildIDs[0] != guildID {
		t.Fatalf("READY guild 列表异常: %v", ready.GuildIDs)
	}
	if ready.ReadStates == nil || len(ready.ReadStates) != 0 {
		t.Fatalf("READY read_states 应为空数组: %+v", ready.ReadStates)
	}

	sendFrame(t, ws, map[string]any{"op": opHeartbeat})
	if f := readFrame(t, ws); f.Op != opHeartbeatACK {
		t.Fatalf("HEARTBEAT 后收到 %s，期待 HEARTBEAT_ACK", f.Op)
	}
}

// TestReadyCarriesGuildSnapshot READY guilds 数组内嵌频道/角色/自身成员/语音状态全量快照。
func TestReadyCarriesGuildSnapshot(t *testing.T) {
	guildID := uuid.New()
	channelID := uuid.New()
	roleID := uuid.New()
	user := model.User{ID: uuid.New(), Username: "alice"}
	memberID := uuid.New()
	snap := snapshot.Guild{
		Guild: model.Guild{ID: guildID, Name: "测试服", OwnerUserID: user.ID},
		Channels: []snapshot.Channel{{
			Channel:     model.Channel{ID: channelID, GuildID: guildID, Name: "大厅", Type: model.ChannelVoice},
			VoiceConfig: &snapshot.VoiceConfig{Mode: model.StageModeFree, MaxSpeakers: 20, RequestToSpeakEnabled: true},
		}},
		Roles: []model.Role{{ID: roleID, GuildID: guildID, Name: "@everyone", IsEveryone: true}},
		Member: snapshot.Member{
			Member:  model.Member{ID: memberID, GuildID: guildID, UserID: user.ID, Nickname: "小A"},
			RoleIDs: []uuid.UUID{roleID},
		},
		VoiceStates: []model.VoiceState{{ID: uuid.New(), GuildID: guildID, UserID: user.ID, ChannelID: &channelID}},
	}
	auth := &stubAuth{users: map[string]model.User{"tok": user}, guildIDs: []uuid.UUID{guildID}}
	dir := &stubDirectory{snapshots: map[uuid.UUID]snapshot.Guild{guildID: snap}}
	h := newHandler(auth, dir, testOptions())
	server := newTestServer(t, h)

	ws := dial(t, server)
	readFrame(t, ws) // HELLO
	sendFrame(t, ws, map[string]any{"op": opIdentify, "d": map[string]string{"token": "tok"}})
	f := readFrame(t, ws)
	var ready readyData
	if err := json.Unmarshal(f.D, &ready); err != nil {
		t.Fatalf("READY 载荷解析失败: %v", err)
	}
	if len(ready.Guilds) != 1 {
		t.Fatalf("READY guilds 数量 = %d，期待 1", len(ready.Guilds))
	}
	got := ready.Guilds[0]
	if got.Guild.ID != guildID || got.Guild.Name != "测试服" {
		t.Fatalf("guild 快照异常: %+v", got.Guild)
	}
	if len(got.Channels) != 1 || got.Channels[0].ID != channelID || got.Channels[0].VoiceConfig == nil {
		t.Fatalf("channels 快照异常: %+v", got.Channels)
	}
	if len(got.Roles) != 1 || got.Roles[0].ID != roleID {
		t.Fatalf("roles 快照异常: %+v", got.Roles)
	}
	if got.Member.Nickname != "小A" || len(got.Member.RoleIDs) != 1 || got.Member.RoleIDs[0] != roleID {
		t.Fatalf("member 快照异常: %+v", got.Member)
	}
	if len(got.VoiceStates) != 1 || got.VoiceStates[0].UserID != user.ID {
		t.Fatalf("voice_states 快照异常: %+v", got.VoiceStates)
	}
}

// TestReadyCarriesReadStates READY read_states 携带该用户可见频道的已读状态；
// 不在快照可见频道集合内的存量记录（禁看/不可见频道）不下发（docs 15 US-8）。
func TestReadyCarriesReadStates(t *testing.T) {
	guildID := uuid.New()
	visibleChannelID := uuid.New()
	hiddenChannelID := uuid.New()
	user := model.User{ID: uuid.New(), Username: "alice"}
	snap := snapshot.Guild{
		Guild: model.Guild{ID: guildID, Name: "测试服"},
		Channels: []snapshot.Channel{{
			Channel: model.Channel{ID: visibleChannelID, GuildID: guildID, Name: "general", Type: model.ChannelText},
		}},
	}
	auth := &stubAuth{users: map[string]model.User{"tok": user}, guildIDs: []uuid.UUID{guildID}}
	dir := &stubDirectory{
		snapshots: map[uuid.UUID]snapshot.Guild{guildID: snap},
		readStates: map[uuid.UUID][]snapshot.ReadState{user.ID: {
			{ChannelID: visibleChannelID, LastReadMessageID: 12345, MentionCount: 2},
			{ChannelID: hiddenChannelID, LastReadMessageID: 999, MentionCount: 7}, // 不可见频道，应被过滤
		}},
	}
	h := newHandler(auth, dir, testOptions())
	server := newTestServer(t, h)

	ws := dial(t, server)
	readFrame(t, ws) // HELLO
	sendFrame(t, ws, map[string]any{"op": opIdentify, "d": map[string]string{"token": "tok"}})
	f := readFrame(t, ws)
	var ready readyData
	if err := json.Unmarshal(f.D, &ready); err != nil {
		t.Fatalf("READY 载荷解析失败: %v", err)
	}
	if len(ready.ReadStates) != 1 {
		t.Fatalf("READY read_states 数量 = %d，期待 1（不可见频道被过滤）: %+v", len(ready.ReadStates), ready.ReadStates)
	}
	got := ready.ReadStates[0]
	if got.ChannelID != visibleChannelID || got.LastReadMessageID != 12345 || got.MentionCount != 2 {
		t.Fatalf("read_states 内容异常: %+v", got)
	}
	// last_read_message_id 以字符串下发（雪花 ID 防 JS 精度丢失）。
	if !strings.Contains(string(f.D), `"last_read_message_id":"12345"`) {
		t.Fatalf("last_read_message_id 应为字符串: %s", f.D)
	}
}

func TestIdentifyTimeoutCloses4001(t *testing.T) {
	h := newHandler(&stubAuth{}, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	ws := dial(t, server)
	if f := readFrame(t, ws); f.Op != opHello {
		t.Fatalf("首帧应为 HELLO")
	}
	// 不发 IDENTIFY，等待服务端超时关闭。
	expectClose(t, ws, closeIdentifyTimeout)
}

func TestIdentifyInvalidTokenCloses4003(t *testing.T) {
	h := newHandler(&stubAuth{users: map[string]model.User{}}, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	ws := dial(t, server)
	readFrame(t, ws) // HELLO
	sendFrame(t, ws, map[string]any{"op": opIdentify, "d": map[string]string{"token": "bad-token"}})
	expectClose(t, ws, closeAuthFailed)
}

func TestFirstFrameNotIdentifyCloses4001(t *testing.T) {
	h := newHandler(&stubAuth{}, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	ws := dial(t, server)
	readFrame(t, ws) // HELLO
	sendFrame(t, ws, map[string]any{"op": opHeartbeat})
	expectClose(t, ws, closeIdentifyTimeout)
}

func TestHeartbeatDeadCloses(t *testing.T) {
	user := model.User{ID: uuid.New(), Username: "alice"}
	h := newHandler(&stubAuth{users: map[string]model.User{"tok": user}}, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	ws, _ := handshake(t, server, "tok")
	// READY 后不再发心跳，两个周期（400ms）后应被判死断开。
	expectClose(t, ws, closeHeartbeatDead)
}

// ---- 事件分发 ----

func TestDispatchTargetedUserIDs(t *testing.T) {
	alice := model.User{ID: uuid.New(), Username: "alice"}
	bob := model.User{ID: uuid.New(), Username: "bob"}
	auth := &stubAuth{users: map[string]model.User{"tok-a": alice, "tok-b": bob}}
	h := newHandler(auth, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	wsAlice1, _ := handshake(t, server, "tok-a")
	wsAlice2, _ := handshake(t, server, "tok-a") // 同一用户第二条连接（独立会话）
	wsBob, _ := handshake(t, server, "tok-b")

	h.hub.dispatch(eventbus.Event{
		Type:    eventbus.EventRestrictionCreate,
		UserIDs: []uuid.UUID{alice.ID},
		Payload: map[string]string{"reason": "违规"},
	})

	// alice 的两条连接都应收到（同用户多连接），且各自会话内 seq 从 1 起。
	for _, ws := range []*websocket.Conn{wsAlice1, wsAlice2} {
		f := readFrame(t, ws)
		if f.Op != opDispatch || f.T != eventbus.EventRestrictionCreate {
			t.Fatalf("收到 op=%s t=%s，期待 DISPATCH RESTRICTION_CREATE", f.Op, f.T)
		}
		if f.S != 1 {
			t.Fatalf("首条 DISPATCH s = %d，期待 1", f.S)
		}
		if !strings.Contains(string(f.D), "违规") {
			t.Fatalf("DISPATCH 载荷异常: %s", f.D)
		}
	}
	// bob 不应收到：发一条心跳，下一帧必须是 ACK 而非 DISPATCH。
	assertNoDispatch(t, wsBob)
}

// TestDispatchSeqMonotonic 同一会话内 DISPATCH 序列号严格递增。
func TestDispatchSeqMonotonic(t *testing.T) {
	user := model.User{ID: uuid.New(), Username: "alice"}
	auth := &stubAuth{users: map[string]model.User{"tok": user}}
	h := newHandler(auth, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	ws, _ := handshake(t, server, "tok")
	for i := 1; i <= 3; i++ {
		h.hub.dispatch(eventbus.Event{
			Type:    eventbus.EventUserUpdate,
			UserIDs: []uuid.UUID{user.ID},
			Payload: map[string]int{"n": i},
		})
	}
	for want := int64(1); want <= 3; want++ {
		f := readFrame(t, ws)
		if f.Op != opDispatch || f.S != want {
			t.Fatalf("收到 op=%s s=%d，期待 DISPATCH s=%d", f.Op, f.S, want)
		}
	}
}

func TestDispatchGuildBroadcastWithVisibilityFilter(t *testing.T) {
	guildID := uuid.New()
	channelID := uuid.New()
	alice := model.User{ID: uuid.New(), Username: "alice"}
	bob := model.User{ID: uuid.New(), Username: "bob"}     // 频道对其不可见
	carol := model.User{ID: uuid.New(), Username: "carol"} // 非 guild 成员
	auth := &stubAuth{users: map[string]model.User{"tok-a": alice, "tok-b": bob, "tok-c": carol}}
	dir := &stubDirectory{
		members:   map[uuid.UUID][]uuid.UUID{guildID: {alice.ID, bob.ID}},
		invisible: map[uuid.UUID]bool{bob.ID: true},
	}
	h := newHandler(auth, dir, testOptions())
	server := newTestServer(t, h)

	wsAlice, _ := handshake(t, server, "tok-a")
	wsBob, _ := handshake(t, server, "tok-b")
	wsCarol, _ := handshake(t, server, "tok-c")

	h.hub.dispatch(eventbus.Event{
		Type:      eventbus.EventMessageCreate,
		GuildID:   &guildID,
		ChannelID: &channelID,
		Payload:   map[string]string{"content": "hello"},
	})

	f := readFrame(t, wsAlice)
	if f.Op != opDispatch || f.T != eventbus.EventMessageCreate {
		t.Fatalf("alice 收到 op=%s t=%s，期待 DISPATCH MESSAGE_CREATE", f.Op, f.T)
	}
	// bob 对频道不可见、carol 非成员，均不应收到。
	assertNoDispatch(t, wsBob)
	assertNoDispatch(t, wsCarol)
}

func TestDispatchGuildBroadcastWithoutChannelSkipsVisibility(t *testing.T) {
	guildID := uuid.New()
	bob := model.User{ID: uuid.New(), Username: "bob"}
	auth := &stubAuth{users: map[string]model.User{"tok-b": bob}}
	// bob 对任意频道不可见，但事件未带 ChannelID → 仍应按 guild 广播送达。
	dir := &stubDirectory{
		members:   map[uuid.UUID][]uuid.UUID{guildID: {bob.ID}},
		invisible: map[uuid.UUID]bool{bob.ID: true},
	}
	h := newHandler(auth, dir, testOptions())
	server := newTestServer(t, h)

	ws, _ := handshake(t, server, "tok-b")
	h.hub.dispatch(eventbus.Event{Type: eventbus.EventPermissionsUpdate, GuildID: &guildID})
	f := readFrame(t, ws)
	if f.Op != opDispatch || f.T != eventbus.EventPermissionsUpdate {
		t.Fatalf("收到 op=%s t=%s，期待 DISPATCH PERMISSIONS_UPDATE", f.Op, f.T)
	}
}

func TestDispatchDropsInternalEvents(t *testing.T) {
	user := model.User{ID: uuid.New(), Username: "alice"}
	auth := &stubAuth{users: map[string]model.User{"tok": user}}
	h := newHandler(auth, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	ws, _ := handshake(t, server, "tok")
	h.hub.dispatch(eventbus.Event{Type: eventbus.InternalCapsDirty, UserIDs: []uuid.UUID{user.ID}})
	assertNoDispatch(t, ws)
}

func TestDispatchViaEventBusSubscription(t *testing.T) {
	user := model.User{ID: uuid.New(), Username: "alice"}
	auth := &stubAuth{users: map[string]model.User{"tok": user}}
	h := newHandler(auth, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	bus := eventbus.New()
	bus.Subscribe(h.hub.dispatch)

	ws, _ := handshake(t, server, "tok")
	bus.Publish(eventbus.Event{Type: eventbus.EventVoiceStateUpdate, UserIDs: []uuid.UUID{user.ID}})
	f := readFrame(t, ws)
	if f.Op != opDispatch || f.T != eventbus.EventVoiceStateUpdate {
		t.Fatalf("收到 op=%s t=%s，期待 DISPATCH VOICE_STATE_UPDATE", f.Op, f.T)
	}
}

// assertNoDispatch 通过「心跳 → 必须直接收到 ACK」验证队列里没有滞留 DISPATCH。
func assertNoDispatch(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	sendFrame(t, ws, map[string]any{"op": opHeartbeat})
	f := readFrame(t, ws)
	if f.Op != opHeartbeatACK {
		t.Fatalf("期待 HEARTBEAT_ACK，实际收到 op=%s t=%s（说明收到了不该推送的事件）", f.Op, f.T)
	}
}

// ---- RESUME / 会话保留 ----

// resumeDial 建立新连接并发送 RESUME 帧（已消费 HELLO）。
func resumeDial(t *testing.T, server *httptest.Server, token, sessionID string, lastSeq int64) *websocket.Conn {
	t.Helper()
	ws := dial(t, server)
	if f := readFrame(t, ws); f.Op != opHello {
		t.Fatalf("首帧 = %s，期待 HELLO", f.Op)
	}
	sendFrame(t, ws, map[string]any{"op": opResume, "d": map[string]any{
		"token": token, "session_id": sessionID, "last_seq": lastSeq,
	}})
	return ws
}

// TestResumeReplaysMissedEvents 断线期间的事件按序补发，随后收到 RESUMED，会话继续可用。
func TestResumeReplaysMissedEvents(t *testing.T) {
	user := model.User{ID: uuid.New(), Username: "alice"}
	auth := &stubAuth{users: map[string]model.User{"tok": user}}
	h := newHandler(auth, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	ws, sessionID := handshake(t, server, "tok")
	h.hub.dispatch(eventbus.Event{Type: eventbus.EventUserUpdate, UserIDs: []uuid.UUID{user.ID}, Payload: map[string]int{"n": 1}})
	if f := readFrame(t, ws); f.S != 1 {
		t.Fatalf("断线前应收到 s=1 的事件，实际 s=%d", f.S)
	}
	_ = ws.Close() // 模拟网络断开

	// 断线期间继续产生事件（进入会话回放缓冲）。
	for i := 2; i <= 3; i++ {
		h.hub.dispatch(eventbus.Event{Type: eventbus.EventUserUpdate, UserIDs: []uuid.UUID{user.ID}, Payload: map[string]int{"n": i}})
	}

	ws2 := resumeDial(t, server, "tok", sessionID, 1)
	for want := int64(2); want <= 3; want++ {
		f := readFrame(t, ws2)
		if f.Op != opDispatch || f.T != eventbus.EventUserUpdate || f.S != want {
			t.Fatalf("补发帧异常 op=%s t=%s s=%d，期待 DISPATCH s=%d", f.Op, f.T, f.S, want)
		}
	}
	if f := readFrame(t, ws2); f.Op != opResumed {
		t.Fatalf("补发后收到 %s，期待 RESUMED", f.Op)
	}
	// 会话继续接收新事件，seq 延续。
	h.hub.dispatch(eventbus.Event{Type: eventbus.EventUserUpdate, UserIDs: []uuid.UUID{user.ID}, Payload: map[string]int{"n": 4}})
	if f := readFrame(t, ws2); f.Op != opDispatch || f.S != 4 {
		t.Fatalf("resume 后新事件异常 op=%s s=%d，期待 DISPATCH s=4", f.Op, f.S)
	}
	// 心跳语义不变。
	assertNoDispatch(t, ws2)
}

// TestResumeNoGapOnlyResumed last_seq 已是最新时无补发，直接 RESUMED。
func TestResumeNoGapOnlyResumed(t *testing.T) {
	user := model.User{ID: uuid.New(), Username: "alice"}
	auth := &stubAuth{users: map[string]model.User{"tok": user}}
	h := newHandler(auth, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	ws, sessionID := handshake(t, server, "tok")
	_ = ws.Close()

	ws2 := resumeDial(t, server, "tok", sessionID, 0)
	if f := readFrame(t, ws2); f.Op != opResumed {
		t.Fatalf("收到 %s，期待 RESUMED", f.Op)
	}
}

// TestResumeUnknownSessionInvalid session 不存在 → INVALID_SESSION + 4009。
func TestResumeUnknownSessionInvalid(t *testing.T) {
	user := model.User{ID: uuid.New(), Username: "alice"}
	auth := &stubAuth{users: map[string]model.User{"tok": user}}
	h := newHandler(auth, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	ws := resumeDial(t, server, "tok", uuid.NewString(), 0)
	if f := readFrame(t, ws); f.Op != opInvalidSession {
		t.Fatalf("收到 %s，期待 INVALID_SESSION", f.Op)
	}
	expectClose(t, ws, closeInvalidSession)
}

// TestResumeWrongUserInvalid 他人 token 冒用 session_id → INVALID_SESSION。
func TestResumeWrongUserInvalid(t *testing.T) {
	alice := model.User{ID: uuid.New(), Username: "alice"}
	bob := model.User{ID: uuid.New(), Username: "bob"}
	auth := &stubAuth{users: map[string]model.User{"tok-a": alice, "tok-b": bob}}
	h := newHandler(auth, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	ws, sessionID := handshake(t, server, "tok-a")
	_ = ws.Close()

	ws2 := resumeDial(t, server, "tok-b", sessionID, 0)
	if f := readFrame(t, ws2); f.Op != opInvalidSession {
		t.Fatalf("收到 %s，期待 INVALID_SESSION", f.Op)
	}
	expectClose(t, ws2, closeInvalidSession)
}

// TestResumeBadTokenCloses4003 RESUME 的 token 无效 → 4003（与 IDENTIFY 一致）。
func TestResumeBadTokenCloses4003(t *testing.T) {
	user := model.User{ID: uuid.New(), Username: "alice"}
	auth := &stubAuth{users: map[string]model.User{"tok": user}}
	h := newHandler(auth, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	ws, sessionID := handshake(t, server, "tok")
	_ = ws.Close()

	ws2 := resumeDial(t, server, "bad-token", sessionID, 0)
	expectClose(t, ws2, closeAuthFailed)
}

// TestResumeBeyondReplayWindow 缺口超出回放缓冲容量 → INVALID_SESSION。
func TestResumeBeyondReplayWindow(t *testing.T) {
	user := model.User{ID: uuid.New(), Username: "alice"}
	auth := &stubAuth{users: map[string]model.User{"tok": user}}
	opts := testOptions()
	opts.ReplayBufferSize = 2 // 只保留最近 2 条
	h := newHandler(auth, &stubDirectory{}, opts)
	server := newTestServer(t, h)

	ws, sessionID := handshake(t, server, "tok")
	_ = ws.Close()

	for i := 1; i <= 4; i++ {
		h.hub.dispatch(eventbus.Event{Type: eventbus.EventUserUpdate, UserIDs: []uuid.UUID{user.ID}, Payload: map[string]int{"n": i}})
	}
	// 缓冲只剩 s=3,4；last_seq=1 需要 s=2 → 缺口不可补。
	ws2 := resumeDial(t, server, "tok", sessionID, 1)
	if f := readFrame(t, ws2); f.Op != opInvalidSession {
		t.Fatalf("收到 %s，期待 INVALID_SESSION", f.Op)
	}
	expectClose(t, ws2, closeInvalidSession)
}

// TestResumeReplacesLiveConnection 旧连接仍在时 RESUME：旧连接被 4006 顶替，新连接接管会话。
func TestResumeReplacesLiveConnection(t *testing.T) {
	user := model.User{ID: uuid.New(), Username: "alice"}
	auth := &stubAuth{users: map[string]model.User{"tok": user}}
	h := newHandler(auth, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	wsOld, sessionID := handshake(t, server, "tok")
	ws2 := resumeDial(t, server, "tok", sessionID, 0)
	if f := readFrame(t, ws2); f.Op != opResumed {
		t.Fatalf("收到 %s，期待 RESUMED", f.Op)
	}
	expectClose(t, wsOld, closeSessionReplaced)

	h.hub.dispatch(eventbus.Event{Type: eventbus.EventUserUpdate, UserIDs: []uuid.UUID{user.ID}, Payload: map[string]int{"n": 1}})
	if f := readFrame(t, ws2); f.Op != opDispatch || f.S != 1 {
		t.Fatalf("接管后事件异常 op=%s s=%d", f.Op, f.S)
	}
}

// TestSessionExpiresAfterResumeWindow 断开超过保留窗口后会话被清理，RESUME 返回 INVALID_SESSION。
func TestSessionExpiresAfterResumeWindow(t *testing.T) {
	user := model.User{ID: uuid.New(), Username: "alice"}
	auth := &stubAuth{users: map[string]model.User{"tok": user}}
	opts := testOptions()
	opts.ResumeWindow = 50 * time.Millisecond
	opts.SweepInterval = 10 * time.Millisecond
	h := newHandler(auth, &stubDirectory{}, opts)
	server := newTestServer(t, h)

	ws, sessionID := handshake(t, server, "tok")
	_ = ws.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, found := h.hub.findSession(sessionID); !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("会话超出保留窗口后未被清理")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sessions := h.hub.userSessions(user.ID); len(sessions) != 0 {
		t.Fatalf("用户索引未清理，剩余 %d 个会话", len(sessions))
	}

	ws2 := resumeDial(t, server, "tok", sessionID, 0)
	if f := readFrame(t, ws2); f.Op != opInvalidSession {
		t.Fatalf("收到 %s，期待 INVALID_SESSION", f.Op)
	}
	expectClose(t, ws2, closeInvalidSession)
}

// TestSessionSurvivesDisconnectWithinWindow 断开后（窗口内）会话保留、连接解绑。
func TestSessionSurvivesDisconnectWithinWindow(t *testing.T) {
	user := model.User{ID: uuid.New(), Username: "alice"}
	auth := &stubAuth{users: map[string]model.User{"tok": user}}
	h := newHandler(auth, &stubDirectory{}, testOptions())
	server := newTestServer(t, h)

	ws, sessionID := handshake(t, server, "tok")
	_ = ws.Close()

	sess, found := h.hub.findSession(sessionID)
	if !found {
		t.Fatalf("断开后会话应保留等待 resume")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		sess.mu.Lock()
		detached := sess.conn == nil
		sess.mu.Unlock()
		if detached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("连接关闭后未从会话解绑")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- 成员缓存 ----

func TestMemberCacheTTL(t *testing.T) {
	guildID := uuid.New()
	userID := uuid.New()
	dir := &stubDirectory{members: map[uuid.UUID][]uuid.UUID{guildID: {userID}}}
	cache := newMemberCache(dir, 50*time.Millisecond)

	for i := 0; i < 3; i++ {
		ids, err := cache.GuildMemberIDs(guildID)
		if err != nil || len(ids) != 1 {
			t.Fatalf("查询成员失败: %v %v", ids, err)
		}
	}
	if dir.memberCalls != 1 {
		t.Fatalf("TTL 内应命中缓存，实际查询 %d 次", dir.memberCalls)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := cache.GuildMemberIDs(guildID); err != nil {
		t.Fatalf("过期后查询失败: %v", err)
	}
	if dir.memberCalls != 2 {
		t.Fatalf("TTL 过期后应回源，实际查询 %d 次", dir.memberCalls)
	}
}
