package presence

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
)

// collector 收集总线事件（分发异步，断言用轮询等待）。
type collector struct {
	mu     sync.Mutex
	events []eventbus.Event
}

func (c *collector) handle(event eventbus.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

// wait 轮询等待满足条件的事件出现。
func (c *collector) wait(t *testing.T, description string, match func(eventbus.Event) bool) eventbus.Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		for _, event := range c.events {
			if match(event) {
				c.mu.Unlock()
				return event
			}
		}
		c.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待事件超时: %s", description)
	return eventbus.Event{}
}

// assertNever 短暂窗口内断言不出现满足条件的事件。
func (c *collector) assertNever(t *testing.T, description string, match func(eventbus.Event) bool) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, event := range c.events {
		if match(event) {
			t.Fatalf("不应出现的事件出现了: %s (%+v)", description, event.Payload)
		}
	}
}

func newTestManager(t *testing.T, others ...uuid.UUID) (*Manager, *collector) {
	t.Helper()
	bus := eventbus.New()
	events := &collector{}
	bus.Subscribe(events.handle)
	manager := NewManager(bus, func(uuid.UUID) ([]uuid.UUID, error) { return others, nil })
	return manager, events
}

// payloadOf 断言事件载荷类型。
func payloadOf(t *testing.T, event eventbus.Event) eventbus.PresenceUpdatePayload {
	t.Helper()
	payload, ok := event.Payload.(eventbus.PresenceUpdatePayload)
	if !ok {
		t.Fatalf("载荷类型异常: %T", event.Payload)
	}
	return payload
}

// targets 判断事件定向包含某用户。
func targets(event eventbus.Event, userID uuid.UUID) bool {
	for _, id := range event.UserIDs {
		if id == userID {
			return true
		}
	}
	return false
}

// TestConnectPublishesOnline 连接默认 online：他人与本人各收到一条。
func TestConnectPublishesOnline(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	m, events := newTestManager(t, other)

	m.Connect(me, "s1", "")
	toOther := events.wait(t, "他人收到 online", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventPresenceUpdate && targets(e, other)
	})
	if p := payloadOf(t, toOther); p.UserID != me || p.Status != StatusOnline {
		t.Fatalf("他人载荷异常: %+v", p)
	}
	toSelf := events.wait(t, "本人收到 online", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventPresenceUpdate && targets(e, me) && len(e.UserIDs) == 1
	})
	if p := payloadOf(t, toSelf); p.Status != StatusOnline {
		t.Fatalf("本人载荷异常: %+v", p)
	}
}

// TestMultiDeviceMergePriority 多端合并：dnd > online > idle > invisible。
func TestMultiDeviceMergePriority(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	m, _ := newTestManager(t, other)

	m.Connect(me, "s1", "")
	m.Connect(me, "s2", "")
	if !m.SetStatus(me, "s1", StatusIdle, "") || !m.SetStatus(me, "s2", StatusInvisible, "") {
		t.Fatal("SetStatus 失败")
	}
	// idle + invisible → idle。
	if got := m.Displayed(other, []uuid.UUID{me})[me].Status; got != StatusIdle {
		t.Fatalf("合并状态 = %s，期待 idle", got)
	}
	m.SetStatus(me, "s2", StatusDnd, "")
	// idle + dnd → dnd。
	if got := m.Displayed(other, []uuid.UUID{me})[me].Status; got != StatusDnd {
		t.Fatalf("合并状态 = %s，期待 dnd", got)
	}
	// 断开 dnd 端 → 剩 idle。
	m.Disconnect(me, "s2")
	if got := m.Displayed(other, []uuid.UUID{me})[me].Status; got != StatusIdle {
		t.Fatalf("合并状态 = %s，期待 idle", got)
	}
}

// TestInvisibleNeverLeaksToOthers 隐身：他人视角 offline（事件与快照均不出现 invisible），
// 本人视角保留真实 invisible。
func TestInvisibleNeverLeaksToOthers(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	m, events := newTestManager(t, other)

	m.Connect(me, "s1", "")
	events.wait(t, "上线广播", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventPresenceUpdate && targets(e, other)
	})
	m.SetStatus(me, "s1", StatusInvisible, "潜水中")

	// 他人收到 offline（且无 custom_text）。
	toOther := events.wait(t, "他人收到 offline", func(e eventbus.Event) bool {
		if e.Type != eventbus.EventPresenceUpdate || !targets(e, other) {
			return false
		}
		p, ok := e.Payload.(eventbus.PresenceUpdatePayload)
		return ok && p.Status == StatusOffline
	})
	if p := payloadOf(t, toOther); p.CustomText != "" {
		t.Fatalf("掩码后的载荷不应携带 custom_text: %+v", p)
	}
	// 本人收到真实 invisible。
	toSelf := events.wait(t, "本人收到 invisible", func(e eventbus.Event) bool {
		if e.Type != eventbus.EventPresenceUpdate || !targets(e, me) || len(e.UserIDs) != 1 {
			return false
		}
		p, ok := e.Payload.(eventbus.PresenceUpdatePayload)
		return ok && p.Status == StatusInvisible
	})
	if p := payloadOf(t, toSelf); p.CustomText != "潜水中" {
		t.Fatalf("本人载荷应保留 custom_text: %+v", p)
	}
	// 发给他人的事件从未出现 invisible。
	events.assertNever(t, "他人收到 invisible", func(e eventbus.Event) bool {
		if e.Type != eventbus.EventPresenceUpdate || !targets(e, other) {
			return false
		}
		p, ok := e.Payload.(eventbus.PresenceUpdatePayload)
		return ok && p.Status == StatusInvisible
	})
	// 快照：他人视角省略（offline），本人视角 invisible。
	if _, ok := m.Displayed(other, []uuid.UUID{me})[me]; ok {
		t.Fatal("他人视角的快照不应包含隐身用户")
	}
	if got := m.Displayed(me, []uuid.UUID{me})[me].Status; got != StatusInvisible {
		t.Fatalf("本人视角快照 = %s，期待 invisible", got)
	}
}

// TestAllSessionsGoneBroadcastsOffline 全部连接注销 → 对外 offline 广播。
func TestAllSessionsGoneBroadcastsOffline(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	m, events := newTestManager(t, other)

	m.Connect(me, "s1", "")
	m.Connect(me, "s2", "")
	m.Disconnect(me, "s1")
	events.assertNever(t, "仍有连接时广播 offline", func(e eventbus.Event) bool {
		p, ok := e.Payload.(eventbus.PresenceUpdatePayload)
		return ok && e.Type == eventbus.EventPresenceUpdate && p.Status == StatusOffline
	})
	m.Disconnect(me, "s2")
	events.wait(t, "他人收到 offline", func(e eventbus.Event) bool {
		if e.Type != eventbus.EventPresenceUpdate || !targets(e, other) {
			return false
		}
		p, ok := e.Payload.(eventbus.PresenceUpdatePayload)
		return ok && p.Status == StatusOffline
	})
	if _, ok := m.Displayed(other, []uuid.UUID{me})[me]; ok {
		t.Fatal("离线用户不应出现在快照中")
	}
}

// TestConnectIdempotentKeepsStatus Connect 幂等：不重置既有状态（RESUME 补登记场景）。
func TestConnectIdempotentKeepsStatus(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	m, _ := newTestManager(t, other)

	m.Connect(me, "s1", "")
	m.SetStatus(me, "s1", StatusDnd, "")
	m.Connect(me, "s1", StatusOnline) // 幂等：不因再次 Connect 覆盖既有 dnd
	if got := m.Displayed(other, []uuid.UUID{me})[me].Status; got != StatusDnd {
		t.Fatalf("Connect 幂等后状态 = %s，期待 dnd", got)
	}
}

// TestConnectWithInitialInvisible 初始状态直接为 invisible：
// mask 前后均为 offline → 不对他人广播（无 online 闪现）；本人见 invisible；
// 快照与事件对他人绝无 invisible。
func TestConnectWithInitialInvisible(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	m, events := newTestManager(t, other)

	m.Connect(me, "s1", StatusInvisible)

	toSelf := events.wait(t, "本人收到 invisible", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventPresenceUpdate && targets(e, me) && len(e.UserIDs) == 1
	})
	if p := payloadOf(t, toSelf); p.Status != StatusInvisible {
		t.Fatalf("本人载荷 = %s，期待 invisible", p.Status)
	}
	// 他人视角：不广播任何 presence（offline→offline 无变化），快照也不含隐身用户。
	events.assertNever(t, "他人收到任何 presence（含 online 闪现 / invisible 泄露）", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventPresenceUpdate && targets(e, other)
	})
	if _, ok := m.Displayed(other, []uuid.UUID{me})[me]; ok {
		t.Fatal("他人视角快照不应包含隐身用户")
	}
	if got := m.Displayed(me, []uuid.UUID{me})[me].Status; got != StatusInvisible {
		t.Fatalf("本人视角快照 = %s，期待 invisible", got)
	}
}

// TestConnectWithInitialDnd 初始 dnd 直接对外可见，无需二次 PRESENCE_UPDATE。
func TestConnectWithInitialDnd(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	m, events := newTestManager(t, other)

	m.Connect(me, "s1", StatusDnd)
	toOther := events.wait(t, "他人收到 dnd", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventPresenceUpdate && targets(e, other)
	})
	if p := payloadOf(t, toOther); p.Status != StatusDnd {
		t.Fatalf("他人载荷 = %s，期待 dnd", p.Status)
	}
}

// TestInvalidStatusRejected 非法状态被拒绝且不产生任何变化。
func TestInvalidStatusRejected(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	m, _ := newTestManager(t, other)

	m.Connect(me, "s1", "")
	if m.SetStatus(me, "s1", "offline", "") || m.SetStatus(me, "s1", "busy", "") {
		t.Fatal("非法状态应被拒绝")
	}
	if got := m.Displayed(other, []uuid.UUID{me})[me].Status; got != StatusOnline {
		t.Fatalf("状态被非法值污染: %s", got)
	}
}

// TestActivitiesMergeAndOmit 活动：写入、省略不改、清空、多端合并。
func TestActivitiesMergeAndOmit(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	m, events := newTestManager(t, other)
	// 测试默认 activityAudience=nil → everyone 可见
	m.Connect(me, "s1", "")
	events.wait(t, "上线", func(e eventbus.Event) bool {
		return e.Type == eventbus.EventPresenceUpdate && targets(e, other)
	})

	acts := []Activity{{Type: ActivityPlaying, Name: "原神", Source: ActivitySourceManual}}
	if !m.SetStatusFull(me, "s1", StatusOnline, CustomStatus{}, &acts) {
		t.Fatal("SetStatusFull 失败")
	}
	toOther := events.wait(t, "他人收到活动", func(e eventbus.Event) bool {
		if e.Type != eventbus.EventPresenceUpdate || !targets(e, other) {
			return false
		}
		p, ok := e.Payload.(eventbus.PresenceUpdatePayload)
		return ok && len(p.Activities) == 1 && p.Activities[0].Name == "原神"
	})
	if p := payloadOf(t, toOther); p.Activities[0].Type != ActivityPlaying {
		t.Fatalf("活动 type 异常: %+v", p.Activities)
	}
	if got := m.Displayed(other, []uuid.UUID{me})[me]; len(got.Activities) != 1 {
		t.Fatalf("快照活动数 = %d", len(got.Activities))
	}

	// 省略 activities：只改 status，活动保留
	if !m.SetStatusFull(me, "s1", StatusIdle, CustomStatus{}, nil) {
		t.Fatal("省略 activities 失败")
	}
	if got := m.Displayed(other, []uuid.UUID{me})[me]; got.Status != StatusIdle || len(got.Activities) != 1 {
		t.Fatalf("省略后应保留活动: status=%s acts=%d", got.Status, len(got.Activities))
	}

	// 清空
	empty := []Activity{}
	if !m.SetStatusFull(me, "s1", StatusIdle, CustomStatus{}, &empty) {
		t.Fatal("清空失败")
	}
	events.wait(t, "他人收到清空活动", func(e eventbus.Event) bool {
		if e.Type != eventbus.EventPresenceUpdate || !targets(e, other) {
			return false
		}
		p, ok := e.Payload.(eventbus.PresenceUpdatePayload)
		return ok && len(p.Activities) == 0 && p.Status == StatusIdle
	})

	// 多端：两端不同游戏，合并
	m.Connect(me, "s2", "")
	a1 := []Activity{{Type: ActivityListening, Name: "Spotify", Source: ActivitySourceMedia}}
	a2 := []Activity{{Type: ActivityPlaying, Name: "Valorant", Source: ActivitySourceDetected}}
	m.SetStatusFull(me, "s1", StatusOnline, CustomStatus{}, &a1)
	m.SetStatusFull(me, "s2", StatusOnline, CustomStatus{}, &a2)
	got := m.Displayed(other, []uuid.UUID{me})[me]
	if len(got.Activities) != 2 {
		t.Fatalf("合并活动数 = %d，期待 2", len(got.Activities))
	}
	// playing 优先于 listening
	if got.Activities[0].Type != ActivityPlaying {
		t.Fatalf("主活动应为 playing，得 %s", got.Activities[0].Type)
	}
}

// TestActivitiesHiddenWhenInvisible 隐身时他人看不到 activities。
func TestActivitiesHiddenWhenInvisible(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	m, events := newTestManager(t, other)
	m.Connect(me, "s1", "")
	acts := []Activity{{Type: ActivityPlaying, Name: "原神", Source: ActivitySourceManual}}
	m.SetStatusFull(me, "s1", StatusOnline, CustomStatus{Text: "嗨"}, &acts)
	events.wait(t, "有活动上线", func(e eventbus.Event) bool {
		p, ok := e.Payload.(eventbus.PresenceUpdatePayload)
		return ok && targets(e, other) && len(p.Activities) == 1
	})
	m.SetStatusFull(me, "s1", StatusInvisible, CustomStatus{Text: "嗨"}, &acts)
	toOther := events.wait(t, "他人 offline", func(e eventbus.Event) bool {
		p, ok := e.Payload.(eventbus.PresenceUpdatePayload)
		return ok && targets(e, other) && p.Status == StatusOffline
	})
	if p := payloadOf(t, toOther); len(p.Activities) != 0 || p.CustomText != "" {
		t.Fatalf("隐身掩码后不应有 custom/activities: %+v", p)
	}
	if _, ok := m.Displayed(other, []uuid.UUID{me})[me]; ok {
		t.Fatal("隐身用户不应出现在他人快照")
	}
	self := m.Displayed(me, []uuid.UUID{me})[me]
	if self.Status != StatusInvisible || len(self.Activities) != 1 {
		t.Fatalf("本人应见 invisible+活动: %+v", self)
	}
}

// TestActivityCoverAssets 封面 assets 随活动广播；非法 URL 被剥离。
func TestActivityCoverAssets(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	m, events := newTestManager(t, other)
	m.Connect(me, "s1", "")
	cover := "https://cdn.cloudflare.steamstatic.com/steam/apps/1443500/header.jpg"
	acts := []Activity{{
		Type: ActivityPlaying, Name: "原神", Source: ActivitySourceManual,
		Assets: &ActivityAssets{LargeImage: cover, LargeText: "原神"},
	}}
	m.SetStatusFull(me, "s1", StatusOnline, CustomStatus{}, &acts)
	toOther := events.wait(t, "他人收到带封面活动", func(e eventbus.Event) bool {
		p, ok := e.Payload.(eventbus.PresenceUpdatePayload)
		return ok && targets(e, other) && len(p.Activities) == 1 &&
			p.Activities[0].Assets != nil && p.Activities[0].Assets.LargeImage == cover
	})
	if p := payloadOf(t, toOther); p.Activities[0].Assets.LargeText != "原神" {
		t.Fatalf("封面文案丢失: %+v", p.Activities[0].Assets)
	}
	// data: URL 应被剥离
	bad := []Activity{{
		Type: ActivityPlaying, Name: "原神", Source: ActivitySourceManual,
		Assets: &ActivityAssets{LargeImage: "data:image/png;base64,AAAA"},
	}}
	m.SetStatusFull(me, "s1", StatusOnline, CustomStatus{}, &bad)
	got := m.Displayed(other, []uuid.UUID{me})[me]
	if len(got.Activities) != 1 {
		t.Fatalf("活动应保留: %+v", got)
	}
	if got.Activities[0].Assets != nil && got.Activities[0].Assets.LargeImage != "" {
		t.Fatalf("非法封面应被剥离: %+v", got.Activities[0].Assets)
	}
}

// TestActivityPrivacyFriends 仅好友可见 activities。
func TestActivityPrivacyFriends(t *testing.T) {
	me, friend, stranger := uuid.New(), uuid.New(), uuid.New()
	m, _ := newTestManager(t, friend, stranger)
	m.SetActivityAudience(func(uuid.UUID) (string, map[uuid.UUID]struct{}) {
		return ShowActivityFriends, map[uuid.UUID]struct{}{friend: {}}
	})
	m.Connect(me, "s1", "")
	acts := []Activity{{Type: ActivityPlaying, Name: "原神", Source: ActivitySourceManual}}
	m.SetStatusFull(me, "s1", StatusOnline, CustomStatus{}, &acts)

	if got := m.Displayed(friend, []uuid.UUID{me})[me]; len(got.Activities) != 1 {
		t.Fatalf("好友应看到活动: %+v", got)
	}
	if got := m.Displayed(stranger, []uuid.UUID{me})[me]; len(got.Activities) != 0 {
		t.Fatalf("非好友不应看到活动: %+v", got)
	}
	// status 仍可见
	if got := m.Displayed(stranger, []uuid.UUID{me})[me]; got.Status != StatusOnline {
		t.Fatalf("非好友仍应看到 online 状态点")
	}
}
