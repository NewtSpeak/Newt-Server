package voice

// 过载自动迁移单测（docs 09 I.3–I.5）：用可注入的假指标源覆盖
// 超阈值持续触发、批量上限、冷却抑制与默认关不动作；
// 迁移对象排序覆盖 N.1（听众/非台上优先）与 N.2（进房时间短优先）。

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
)

func overloadedNode(id uuid.UUID, cpu float64) sfuctl.NodeInfo {
	return sfuctl.NodeInfo{
		ID: id, Status: "ONLINE", Online: true, EnabledForScheduling: true,
		MaxUsers: 100, CurrentUsers: 50, CPUPercent: cpu,
	}
}

// TestNodeOverloaded 任一维度超阈值即命中；离线/排空节点不参与。
func TestNodeOverloaded(t *testing.T) {
	cfg := defaultOverloadConfig()
	id := uuid.New()
	if nodeOverloaded(overloadedNode(id, 50), cfg) {
		t.Error("CPU 50% 不应判过载")
	}
	if !nodeOverloaded(overloadedNode(id, 90), cfg) {
		t.Error("CPU 90% 应判过载")
	}
	// 并发维度：90/100 ≥ 0.9。
	byUsers := overloadedNode(id, 10)
	byUsers.CurrentUsers = 90
	if !nodeOverloaded(byUsers, cfg) {
		t.Error("并发 90% 应判过载")
	}
	// 带宽维度默认关（阈值 ≤0）。
	byBW := overloadedNode(id, 10)
	byBW.BandwidthOutMbps = 5000
	if nodeOverloaded(byBW, cfg) {
		t.Error("带宽阈值未配置时不应按带宽判过载")
	}
	cfg.BandwidthOutMbpsThreshold = 1000
	if !nodeOverloaded(byBW, cfg) {
		t.Error("带宽超阈值应判过载")
	}
	// 排空/离线不参与。
	draining := overloadedNode(id, 99)
	draining.Draining = true
	if nodeOverloaded(draining, cfg) {
		t.Error("DRAINING 节点不应参与过载判定")
	}
}

// TestOverloadBatchSize ≤15% 且 ≤50 取更严（docs 09 I.5）；小节点放行 1 人。
func TestOverloadBatchSize(t *testing.T) {
	cfg := defaultOverloadConfig()
	cases := []struct{ sessions, want int }{
		{0, 0},
		{3, 1},     // floor(0.45)=0 → 放行最小迁移单位 1
		{100, 15},  // 15%
		{1000, 50}, // 50 人硬顶更严
	}
	for _, tc := range cases {
		if got := overloadBatchSize(tc.sessions, cfg); got != tc.want {
			t.Errorf("sessions=%d: got %d want %d", tc.sessions, got, tc.want)
		}
	}
}

// TestOverloadDetectorSustainAndCooldown 超阈值须持续 T 才触发；触发后冷却期抑制；
// 恢复正常清零重新计时；默认关不动作。
func TestOverloadDetectorSustainAndCooldown(t *testing.T) {
	cfg := defaultOverloadConfig()
	cfg.Enabled = true
	cfg.Sustain = 30 * time.Second
	cfg.Cooldown = 60 * time.Second
	d := newOverloadDetector(cfg)
	nodeID := uuid.New()
	t0 := time.Now()
	hot := []sfuctl.NodeInfo{overloadedNode(nodeID, 95)}
	cool := []sfuctl.NodeInfo{overloadedNode(nodeID, 10)}

	// 首次观察到超阈值：只记起点，不触发。
	if fire := d.evaluate(hot, t0); len(fire) != 0 {
		t.Fatalf("首次超阈值不应触发: %v", fire)
	}
	// 未满 T：不触发。
	if fire := d.evaluate(hot, t0.Add(20*time.Second)); len(fire) != 0 {
		t.Fatalf("未持续满 T 不应触发: %v", fire)
	}
	// 满 T：触发一轮并进入冷却。
	if fire := d.evaluate(hot, t0.Add(31*time.Second)); len(fire) != 1 || fire[0] != nodeID {
		t.Fatalf("持续满 T 应触发: %v", fire)
	}
	// 冷却期内持续过载：抑制。
	if fire := d.evaluate(hot, t0.Add(60*time.Second)); len(fire) != 0 {
		t.Fatalf("冷却期内不应再触发: %v", fire)
	}
	// 冷却结束仍过载：再触发一轮。
	if fire := d.evaluate(hot, t0.Add(95*time.Second)); len(fire) != 1 {
		t.Fatalf("冷却结束后应再评估触发: %v", fire)
	}
	// 恢复正常：清零；随后再超阈值须重新持续满 T。
	d.evaluate(cool, t0.Add(160*time.Second))
	if fire := d.evaluate(hot, t0.Add(161*time.Second)); len(fire) != 0 {
		t.Fatalf("恢复后重新超阈值应重新计时: %v", fire)
	}

	// 默认关：任何情况不动作。
	off := newOverloadDetector(defaultOverloadConfig()) // Enabled=false
	off.evaluate(hot, t0)
	if fire := off.evaluate(hot, t0.Add(time.Hour)); len(fire) != 0 {
		t.Fatalf("默认关不应触发: %v", fire)
	}
}

// TestOrderOverloadVictims N.1 听众/非台上优先；N.2 同级内进房时间短优先。
func TestOrderOverloadVictims(t *testing.T) {
	speaker, oldAud, newAud := uuid.New(), uuid.New(), uuid.New()
	early := time.Now().Add(-time.Hour)
	late := time.Now().Add(-time.Minute)
	states := []model.VoiceState{
		{UserID: speaker, JoinedAt: &late},
		{UserID: oldAud, JoinedAt: &early},
		{UserID: newAud, JoinedAt: &late},
	}
	got := orderOverloadVictims(states, map[uuid.UUID]bool{speaker: true})
	want := []uuid.UUID{newAud, oldAud, speaker}
	for i, vs := range got {
		if vs.UserID != want[i] {
			t.Fatalf("排序第 %d 位应为 %s，got %s（期望：新进房听众 → 老听众 → 台上）",
				i, want[i], vs.UserID)
		}
	}
}

// TestMigrateOverloadedNodeCreatesCappedJobs 触发一轮批量：job 数 = min(15%,50)、
// reason=OVERLOAD、台上发言者最后才被选中（需要 TEST_DATABASE_URL）。
func TestMigrateOverloadedNodeCreatesCappedJobs(t *testing.T) {
	hotNode, targetNode := uuid.New(), uuid.New()
	svc := newTestService(t, []sfuctl.NodeInfo{testNode(hotNode), testNode(targetNode)})
	if err := svc.db.AutoMigrate(&model.StageSpeaker{}); err != nil {
		t.Fatalf("迁移 StageSpeaker 失败: %v", err)
	}
	svc.overload = newOverloadDetector(overloadConfig{
		Enabled: true, BatchRatio: 0.15, BatchMax: 50,
	})
	guildID, channelID := uuid.New(), uuid.New()

	// 20 个会话（15% → 3 人）；其中 1 个台上 SPEAKER 不应被选中。
	speakerID := uuid.New()
	users := []uuid.UUID{speakerID}
	for i := 0; i < 19; i++ {
		users = append(users, uuid.New())
	}
	base := time.Now().Add(-time.Hour)
	for i, u := range users {
		joined := base.Add(time.Duration(i) * time.Minute)
		svc.db.Create(&model.VoiceState{
			ID: uuid.New(), GuildID: guildID, UserID: u,
			ChannelID: &channelID, NodeID: &hotNode, RoomID: &channelID, JoinedAt: &joined,
		})
	}
	svc.db.Create(&model.StageSpeaker{
		ID: uuid.New(), GuildID: guildID, ChannelID: channelID, UserID: speakerID,
		GrantedAt: time.Now(),
	})

	svc.migrateOverloadedNode(hotNode)

	var jobs []model.VoiceMigrationJob
	svc.db.Find(&jobs, "guild_id = ?", guildID)
	if len(jobs) != 3 {
		t.Fatalf("20 会话 ×15%% 应创建 3 个 job，got %d", len(jobs))
	}
	for _, job := range jobs {
		if job.Reason != model.MigrationReasonOverload {
			t.Fatalf("reason 应为 OVERLOAD，got %s", job.Reason)
		}
		if job.UserID == speakerID {
			t.Fatal("台上 SPEAKER 不应进入过载批量（docs 09 N.1）")
		}
		if job.BatchKey != hotNode.String()+"@"+channelID.String() {
			t.Fatalf("batch_key 不符: %s", job.BatchKey)
		}
	}
}
