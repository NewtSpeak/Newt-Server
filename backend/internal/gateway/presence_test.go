package gateway

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/presence"
	"github.com/owlspeak/owl-server/backend/internal/snapshot"
)

// presenceEnv 组装带 presence 的 Gateway 测试环境：alice / bob 互为共享 guild 成员。
type presenceEnv struct {
	handler *handler
	alice   model.User
	bob     model.User
	guildID uuid.UUID
}

func newPresenceEnv(t *testing.T, opts options) *presenceEnv {
	t.Helper()
	guildID := uuid.New()
	alice := model.User{ID: uuid.New(), Username: "alice"}
	bob := model.User{ID: uuid.New(), Username: "bob"}
	auth := &stubAuth{users: map[string]model.User{"tok-a": alice, "tok-b": bob}, guildIDs: []uuid.UUID{guildID}}
	dir := &stubDirectory{
		members: map[uuid.UUID][]uuid.UUID{guildID: {alice.ID, bob.ID}},
		snapshots: map[uuid.UUID]snapshot.Guild{guildID: {
			Guild: model.Guild{ID: guildID, Name: "测试服"}, Presences: []snapshot.Presence{},
		}},
	}
	bus := eventbus.New()
	manager := presence.NewManager(bus, func(userID uuid.UUID) ([]uuid.UUID, error) {
		if userID == alice.ID {
			return []uuid.UUID{bob.ID}, nil
		}
		return []uuid.UUID{alice.ID}, nil
	})
	h := newHandler(auth, dir, opts)
	h.attachPresence(manager)
	bus.Subscribe(h.hub.dispatch)
	return &presenceEnv{handler: h, alice: alice, bob: bob, guildID: guildID}
}

// presencePayload PRESENCE_UPDATE 下行载荷的测试视图。
type presencePayload struct {
	UserID     uuid.UUID `json:"user_id"`
	Status     string    `json:"status"`
	CustomText string    `json:"custom_text"`
}

// waitPresence 读帧直到出现满足条件的 PRESENCE_UPDATE；期间收到的其他帧忽略，
// 但若出现 forbid 条件（如 invisible 泄露）立即失败。
func waitPresence(t *testing.T, ws *websocket.Conn, match, forbid func(presencePayload) bool) presencePayload {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f := readFrame(t, ws)
		if f.Op != opDispatch || f.T != eventbus.EventPresenceUpdate {
			continue
		}
		var p presencePayload
		if err := json.Unmarshal(f.D, &p); err != nil {
			t.Fatalf("解析 PRESENCE_UPDATE 载荷失败: %v (%s)", err, f.D)
		}
		if forbid != nil && forbid(p) {
			t.Fatalf("收到不应出现的 PRESENCE_UPDATE: %+v", p)
		}
		if match(p) {
			return p
		}
	}
	t.Fatal("等待 PRESENCE_UPDATE 超时")
	return presencePayload{}
}

// TestGatewayPresenceUpdateOpBroadcasts 上行 PRESENCE_UPDATE：共享 guild 的他人
// 收到合并后的状态，本人端收到真实状态。
func TestGatewayPresenceUpdateOpBroadcasts(t *testing.T) {
	env := newPresenceEnv(t, testOptions())
	server := newTestServer(t, env.handler)

	wsAlice, _ := handshake(t, server, "tok-a")
	wsBob, _ := handshake(t, server, "tok-b")

	// alice 先看到 bob 上线（IDENTIFY 默认 online）。
	waitPresence(t, wsAlice, func(p presencePayload) bool {
		return p.UserID == env.bob.ID && p.Status == "online"
	}, nil)

	// bob 切 dnd → alice 与 bob 本人都收到 dnd。
	sendFrame(t, wsBob, map[string]any{"op": opPresenceUpdate, "d": map[string]string{"status": "dnd", "custom_text": "开会"}})
	got := waitPresence(t, wsAlice, func(p presencePayload) bool {
		return p.UserID == env.bob.ID && p.Status == "dnd"
	}, nil)
	if got.CustomText != "开会" {
		t.Fatalf("custom_text 未随事件下发: %+v", got)
	}
	waitPresence(t, wsBob, func(p presencePayload) bool {
		return p.UserID == env.bob.ID && p.Status == "dnd"
	}, nil)
}

// TestGatewayInvisibleMaskedAsOffline 隐身：他人收到 offline（全程绝无 invisible），
// 本人端收到真实 invisible。
func TestGatewayInvisibleMaskedAsOffline(t *testing.T) {
	env := newPresenceEnv(t, testOptions())
	server := newTestServer(t, env.handler)

	wsAlice, _ := handshake(t, server, "tok-a")
	wsBob, _ := handshake(t, server, "tok-b")

	sendFrame(t, wsBob, map[string]any{"op": opPresenceUpdate, "d": map[string]string{"status": "invisible"}})
	// alice 只能看到 bob offline；若泄露 invisible 立即失败。
	waitPresence(t, wsAlice, func(p presencePayload) bool {
		return p.UserID == env.bob.ID && p.Status == "offline"
	}, func(p presencePayload) bool {
		return p.UserID == env.bob.ID && p.Status == "invisible"
	})
	// bob 本人端看到真实 invisible。
	waitPresence(t, wsBob, func(p presencePayload) bool {
		return p.UserID == env.bob.ID && p.Status == "invisible"
	}, nil)
}

// TestGatewayReadyCarriesPresences 后连接者的 READY 快照附带各 guild 在线成员状态。
func TestGatewayReadyCarriesPresences(t *testing.T) {
	env := newPresenceEnv(t, testOptions())
	server := newTestServer(t, env.handler)

	wsAlice, _ := handshake(t, server, "tok-a")
	sendFrame(t, wsAlice, map[string]any{"op": opPresenceUpdate, "d": map[string]string{"status": "dnd"}})
	// 等 alice 本人端收到 dnd 回执，确保状态已生效再让 bob 连接。
	waitPresence(t, wsAlice, func(p presencePayload) bool {
		return p.UserID == env.alice.ID && p.Status == "dnd"
	}, nil)

	ws := dial(t, server)
	readFrame(t, ws) // HELLO
	sendFrame(t, ws, map[string]any{"op": opIdentify, "d": map[string]string{"token": "tok-b"}})
	f := readFrame(t, ws)
	if f.Op != opReady {
		t.Fatalf("IDENTIFY 后收到 %s，期待 READY", f.Op)
	}
	var ready readyData
	if err := json.Unmarshal(f.D, &ready); err != nil {
		t.Fatalf("READY 载荷解析失败: %v", err)
	}
	if len(ready.Guilds) != 1 {
		t.Fatalf("READY guilds 数量 = %d，期待 1", len(ready.Guilds))
	}
	statuses := map[uuid.UUID]string{}
	for _, p := range ready.Guilds[0].Presences {
		statuses[p.UserID] = p.Status
	}
	if statuses[env.alice.ID] != "dnd" {
		t.Fatalf("READY presences 中 alice = %q，期待 dnd（全部: %v）", statuses[env.alice.ID], statuses)
	}
	if statuses[env.bob.ID] != "online" {
		t.Fatalf("READY presences 中 bob 本人 = %q，期待 online", statuses[env.bob.ID])
	}
}

// TestGatewayOfflineAfterResumeWindow 连接断开且 resume 窗口结束 → 对外广播 offline。
func TestGatewayOfflineAfterResumeWindow(t *testing.T) {
	opts := testOptions()
	opts.ResumeWindow = 50 * time.Millisecond
	opts.SweepInterval = 10 * time.Millisecond
	env := newPresenceEnv(t, opts)
	server := newTestServer(t, env.handler)

	wsAlice, _ := handshake(t, server, "tok-a")
	wsBob, _ := handshake(t, server, "tok-b")
	waitPresence(t, wsAlice, func(p presencePayload) bool {
		return p.UserID == env.bob.ID && p.Status == "online"
	}, nil)

	_ = wsBob.Close()
	// resume 窗口（50ms）结束、会话被清扫后，alice 收到 bob offline。
	waitPresence(t, wsAlice, func(p presencePayload) bool {
		return p.UserID == env.bob.ID && p.Status == "offline"
	}, nil)
}
