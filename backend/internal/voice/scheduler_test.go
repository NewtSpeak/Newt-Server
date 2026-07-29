package voice

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/sfuctl"
)

func onlineNode(id uuid.UUID, region string, maxUsers, current int) sfuctl.NodeInfo {
	return sfuctl.NodeInfo{
		ID: id, Region: region, Status: "ONLINE", Online: true,
		EnabledForScheduling: true, MaxUsers: maxUsers, CurrentUsers: current,
	}
}

// TestPassHardFilter 硬过滤表驱动（docs 10 §3）。
func TestPassHardFilter(t *testing.T) {
	nodeID := uuid.New()
	fromID := uuid.New()
	cfg := defaultSchedConfig()

	cases := []struct {
		name string
		cand nodeCandidate
		p    scheduleParams
		want bool
	}{
		{
			name: "健康在线节点通过",
			cand: nodeCandidate{Info: onlineNode(nodeID, "ap-east", 100, 10)},
			p:    scheduleParams{Mode: ModeJoin, Config: cfg},
			want: true,
		},
		{
			name: "离线节点拒绝",
			cand: func() nodeCandidate {
				c := nodeCandidate{Info: onlineNode(nodeID, "ap-east", 100, 10)}
				c.Info.Online = false
				return c
			}(),
			p:    scheduleParams{Mode: ModeJoin, Config: cfg},
			want: false,
		},
		{
			name: "状态非 ONLINE 拒绝",
			cand: func() nodeCandidate {
				c := nodeCandidate{Info: onlineNode(nodeID, "ap-east", 100, 10)}
				c.Info.Status = "ENROLLED"
				return c
			}(),
			p:    scheduleParams{Mode: ModeJoin, Config: cfg},
			want: false,
		},
		{
			name: "DRAINING 硬拒",
			cand: func() nodeCandidate {
				c := nodeCandidate{Info: onlineNode(nodeID, "ap-east", 100, 10)}
				c.Info.Draining = true
				return c
			}(),
			p:    scheduleParams{Mode: ModeMigrateLeaf, Config: cfg},
			want: false,
		},
		{
			name: "未启用调度拒绝",
			cand: func() nodeCandidate {
				c := nodeCandidate{Info: onlineNode(nodeID, "ap-east", 100, 10)}
				c.Info.EnabledForScheduling = false
				return c
			}(),
			p:    scheduleParams{Mode: ModeJoin, Config: cfg},
			want: false,
		},
		{
			name: "迁移排除源节点",
			cand: nodeCandidate{Info: onlineNode(fromID, "ap-east", 100, 10)},
			p:    scheduleParams{Mode: ModeMigrateLeaf, FromNodeID: &fromID, Config: cfg},
			want: false,
		},
		{
			name: "合规标签不匹配硬拒",
			cand: nodeCandidate{Info: onlineNode(nodeID, "ap-east", 100, 10)},
			p: scheduleParams{Mode: ModeJoin, Config: cfg,
				MustMatch: map[string]string{"compliance": "in_country"}},
			want: false,
		},
		{
			name: "合规标签匹配通过",
			cand: func() nodeCandidate {
				c := nodeCandidate{Info: onlineNode(nodeID, "ap-east", 100, 10)}
				c.Info.Labels = map[string]string{"compliance": "in_country"}
				return c
			}(),
			p: scheduleParams{Mode: ModeJoin, Config: cfg,
				MustMatch: map[string]string{"compliance": "in_country"}},
			want: true,
		},
		{
			// max=100 → 预留 5；Join 上限 95：当前 94 可进（94+1<=95）。
			name: "Join 不吃迁移预留-边界内通过",
			cand: nodeCandidate{Info: onlineNode(nodeID, "ap-east", 100, 94)},
			p:    scheduleParams{Mode: ModeJoin, Config: cfg},
			want: true,
		},
		{
			// 当前 95：95+1 > 95 → Join 拒绝，但迁移可以吃预留。
			name: "Join 吃到预留区拒绝",
			cand: nodeCandidate{Info: onlineNode(nodeID, "ap-east", 100, 95)},
			p:    scheduleParams{Mode: ModeJoin, Config: cfg},
			want: false,
		},
		{
			name: "迁移可用预留区",
			cand: nodeCandidate{Info: onlineNode(nodeID, "ap-east", 100, 95)},
			p:    scheduleParams{Mode: ModeMigrateLeaf, Config: cfg},
			want: true,
		},
		{
			name: "迁移也不能超过总容量",
			cand: nodeCandidate{Info: onlineNode(nodeID, "ap-east", 100, 100)},
			p:    scheduleParams{Mode: ModeMigrateBatch, Config: cfg},
			want: false,
		},
		{
			// 活跃预留计入占用：94 + 2 预留 + 1 > 95。
			name: "活跃容量预留计入 Join 占用",
			cand: nodeCandidate{Info: onlineNode(nodeID, "ap-east", 100, 93), Reserved: 2},
			p:    scheduleParams{Mode: ModeJoin, Config: cfg},
			want: false,
		},
		{
			name: "无容量信息拒绝",
			cand: nodeCandidate{Info: onlineNode(nodeID, "ap-east", 0, 0)},
			p:    scheduleParams{Mode: ModeJoin, Config: cfg},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := passHardFilter(tc.cand, tc.p); got != tc.want {
				t.Fatalf("passHardFilter=%v，期望 %v", got, tc.want)
			}
		})
	}
}

// TestMigrationReserve 预留 = max(1, floor(max*0.05))。
func TestMigrationReserve(t *testing.T) {
	cases := []struct{ max, want int }{
		{100, 5}, {10, 1}, {5, 1}, {1, 1}, {200, 10}, {59, 2},
	}
	for _, tc := range cases {
		if got := migrationReserve(tc.max, 0.05); got != tc.want {
			t.Fatalf("migrationReserve(%d)=%d，期望 %d", tc.max, got, tc.want)
		}
	}
}

// TestScoreRTT 无样本 = 0.5；有样本越低越高；clamp 生效。
func TestScoreRTT(t *testing.T) {
	cfg := defaultSchedConfig()
	if got := scoreRTT(0, false, cfg); got != 0.5 {
		t.Fatalf("无样本应为 0.5，得 %v", got)
	}
	low := scoreRTT(20, true, cfg)
	high := scoreRTT(200, true, cfg)
	if low <= high {
		t.Fatalf("低 RTT 应得更高分：low=%v high=%v", low, high)
	}
	if got := scoreRTT(5000, true, cfg); got != 0 {
		t.Fatalf("超高 RTT 应为 0，得 %v", got)
	}
	if got := scoreRTT(1, true, cfg); got != 1 {
		t.Fatalf("clamp 下限后应为 1，得 %v", got)
	}
}

// TestScoreRegion 完全匹配 > 同大区 > 异区；未知 region 中性。
func TestScoreRegion(t *testing.T) {
	if scoreRegion("ap-east", "ap-east") != 1 {
		t.Fatal("同 region 应为 1")
	}
	if scoreRegion("ap-east", "ap-west") != 0.5 {
		t.Fatal("同大区应为 0.5")
	}
	if scoreRegion("ap-east", "eu-west") != 0 {
		t.Fatal("异大区应为 0")
	}
	if scoreRegion("", "ap-east") != 0.5 {
		t.Fatal("未知客户端 region 应中性 0.5")
	}
}

// TestScoreSticky 同房人数饱和与软顶降分（docs 10 T.4）。
func TestScoreSticky(t *testing.T) {
	cfg := defaultSchedConfig() // saturation=8, softcap=80
	if scoreSticky(0, cfg) != 0 {
		t.Fatal("无同房用户应为 0")
	}
	if scoreSticky(4, cfg) != 0.5 {
		t.Fatal("4/8 应为 0.5")
	}
	if scoreSticky(20, cfg) != 1 {
		t.Fatal("超过饱和应为 1")
	}
	if scoreSticky(80, cfg) != 0 {
		t.Fatal("达到软顶应降为 0")
	}
}

// TestScheduleOrdering RTT 优的节点应成为 JOIN primary；其余进 fallbacks。
func TestScheduleOrdering(t *testing.T) {
	nearID := uuid.New()
	farID := uuid.New()
	userID := uuid.New()
	candidates := []nodeCandidate{
		{Info: onlineNode(farID, "eu-west", 100, 10)},
		{Info: onlineNode(nearID, "ap-east", 100, 10)},
	}
	result, ok := schedule(candidates, scheduleParams{
		Mode: ModeJoin, UserID: userID, ClientRegion: "ap-east",
		RTTMs:  map[uuid.UUID]float64{nearID: 20, farID: 250},
		Config: defaultSchedConfig(),
		// 测试关闭微扰保证可复现。
	})
	if !ok {
		t.Fatal("应有调度结果")
	}
	if result.Primary != nearID {
		t.Fatalf("primary 应为低 RTT 同区节点，得 %s", result.Primary)
	}
	if len(result.Fallbacks) != 1 || result.Fallbacks[0] != farID {
		t.Fatalf("fallbacks 应含另一节点，得 %v", result.Fallbacks)
	}
}

// TestScheduleStickyForBatch MIGRATE_BATCH 下同房用户多的节点应胜过 RTT 略优的空节点。
func TestScheduleStickyForBatch(t *testing.T) {
	stickyID := uuid.New()
	emptyID := uuid.New()
	from := uuid.New()
	candidates := []nodeCandidate{
		{Info: onlineNode(stickyID, "ap-east", 200, 50), SameRoomUsers: 8, OnTree: true},
		{Info: onlineNode(emptyID, "ap-east", 200, 10)},
	}
	result, ok := schedule(candidates, scheduleParams{
		Mode: ModeMigrateBatch, UserID: uuid.New(), FromNodeID: &from,
		RTTMs:  map[uuid.UUID]float64{stickyID: 80, emptyID: 40},
		Config: defaultSchedConfig(),
	})
	if !ok {
		t.Fatal("应有调度结果")
	}
	if result.Primary != stickyID {
		t.Fatalf("BATCH 模式应偏向同房粘性节点，得 %s", result.Primary)
	}
}

// TestScheduleNoCandidate 全部被硬过滤时返回 ok=false（上层 503 / 排队重试）。
func TestScheduleNoCandidate(t *testing.T) {
	draining := nodeCandidate{Info: onlineNode(uuid.New(), "ap-east", 100, 10)}
	draining.Info.Draining = true
	_, ok := schedule([]nodeCandidate{draining}, scheduleParams{Mode: ModeJoin, UserID: uuid.New(), Config: defaultSchedConfig()})
	if ok {
		t.Fatal("DRAINING 节点不应产出结果")
	}
}

// TestScheduleTieBreakStable 同分时 hash 打散但对同一用户稳定。
func TestScheduleTieBreakStable(t *testing.T) {
	a := onlineNode(uuid.New(), "ap-east", 100, 10)
	b := onlineNode(a.ID, "ap-east", 100, 10)
	b.ID = uuid.New()
	userID := uuid.New()
	p := scheduleParams{Mode: ModeMigrateLeaf, UserID: userID, Config: defaultSchedConfig()}
	first, ok1 := schedule([]nodeCandidate{{Info: a}, {Info: b}}, p)
	second, ok2 := schedule([]nodeCandidate{{Info: b}, {Info: a}}, p)
	if !ok1 || !ok2 {
		t.Fatal("应有调度结果")
	}
	if first.Primary != second.Primary {
		t.Fatalf("同分 tie-break 应与候选顺序无关：%s vs %s", first.Primary, second.Primary)
	}
}

// TestRTTStoreTTL 样本 60s TTL 与 clamp。
func TestRTTStoreTTL(t *testing.T) {
	store := newRTTStore(defaultSchedConfig())
	userID, nodeID := uuid.New(), uuid.New()
	base := time.Now()
	store.Report(userID, nodeID, 3, base) // clamp 到 5
	samples := store.Samples(userID, base)
	if samples[nodeID] != 5 {
		t.Fatalf("RTT 应 clamp 到 5，得 %v", samples[nodeID])
	}
	if len(store.Samples(userID, base.Add(61*time.Second))) != 0 {
		t.Fatal("超过 TTL 的样本应被清除")
	}
}

// TestReservationStore 预留 TTL 自动释放与计数。
func TestReservationStore(t *testing.T) {
	store := newReservationStore()
	nodeID := uuid.New()
	id := store.Reserve(nodeID, 50*time.Millisecond)
	if store.ActiveByNode()[nodeID] != 1 {
		t.Fatal("应有 1 个活跃预留")
	}
	store.Release(id)
	if store.ActiveByNode()[nodeID] != 0 {
		t.Fatal("释放后应为 0")
	}
	store.Reserve(nodeID, time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if store.ActiveByNode()[nodeID] != 0 {
		t.Fatal("过期预留应自动清理")
	}
}

// TestWeightsNormalized 各 mode 权重和为 1。
func TestWeightsNormalized(t *testing.T) {
	for _, mode := range []ScheduleMode{ModeJoin, ModeMigrateLeaf, ModeMigrateBatch} {
		w := weightsFor(mode)
		sum := w.RTT + w.Region + w.Capacity + w.Sticky + w.Tree
		if sum < 0.999 || sum > 1.001 {
			t.Fatalf("mode %s 权重和 %v ≠ 1", mode, sum)
		}
	}
}
