package voice

// 迁移引擎数据库集成测试：覆盖优先级抢占与同 user 单 job 锁、batch 同目标收敛、
// 段超时推进与重试换目标（docs 09 K.3–K.5、docs 10 U.2）。
//
// 需要真实 PostgreSQL（gorm 无内存驱动），默认跳过：
//
//	TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/owl_test?sslmode=disable' \
//	  go test ./internal/voice/
//
// 测试使用随机 ID，可对同一数据库重复运行；会执行 AutoMigrate（勿指向生产库）。

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// fakeDirectory 固定节点集的 sfuctl.Directory 桩。
type fakeDirectory struct{ nodes []sfuctl.NodeInfo }

func (d fakeDirectory) PoolNodes(uuid.UUID) ([]sfuctl.NodeInfo, error) { return d.nodes, nil }
func (d fakeDirectory) Node(id uuid.UUID) (sfuctl.NodeInfo, error) {
	for _, n := range d.nodes {
		if n.ID == id {
			return n, nil
		}
	}
	return sfuctl.NodeInfo{}, sfuctl.ErrNodeNotFound
}
func (d fakeDirectory) AllNodes() ([]sfuctl.NodeInfo, error) { return d.nodes, nil }

// fakeController 记录调用的 sfuctl.Controller 桩（全部成功）。
type fakeController struct{ migrateCalls []string }

func (c *fakeController) EnsureRoom(uuid.UUID, uuid.UUID) error                  { return nil }
func (c *fakeController) CloseRoom(uuid.UUID, uuid.UUID) error                   { return nil }
func (c *fakeController) DisconnectUser(uuid.UUID, uuid.UUID, uuid.UUID, string) error { return nil }
func (c *fakeController) UpdateParticipantCaps(uuid.UUID, uuid.UUID, uuid.UUID, []string) error {
	return nil
}
func (c *fakeController) SetAnchorLease(uuid.UUID, uuid.UUID, uuid.UUID, uint64, time.Time) error {
	return nil
}
func (c *fakeController) SetCascadeEdges(uuid.UUID, uint64, []sfuctl.Edge) error { return nil }
func (c *fakeController) WaitEdgeUp(uuid.UUID, uint64, uuid.UUID, uuid.UUID, time.Duration) error {
	return nil
}
func (c *fakeController) DrainNode(uuid.UUID, time.Time) error { return nil }
func (c *fakeController) UndrainNode(uuid.UUID) error          { return nil }
func (c *fakeController) MigrateParticipants(_, _ uuid.UUID, _ uuid.UUID, sids []string, _ uuid.UUID, phase sfuctl.MigratePhase) error {
	c.migrateCalls = append(c.migrateCalls, string(phase)+":"+fmt.Sprint(len(sids)))
	return nil
}

// newTestService 打开测试库并构造最小 Service（不启动引擎循环与总线订阅）。
func newTestService(t *testing.T, nodes []sfuctl.NodeInfo) *Service {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过数据库集成测试（说明见文件头注释）")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.VoiceState{}, &model.VoiceAnchorLease{},
		&model.VoiceCascadeEdge{}, &model.VoiceMigrationJob{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	// 注入 sfuctl 桩（全局，测试结束恢复 noop）。
	sfuctl.SetDirectory(fakeDirectory{nodes: nodes})
	sfuctl.SetController(&fakeController{})
	t.Cleanup(func() {
		sfuctl.SetDirectory(fakeDirectory{})
		sfuctl.SetController(&fakeController{})
	})
	sched := defaultSchedConfig()
	svc := &Service{
		db: db, bus: eventbus.New(),
		rtt: newRTTStore(sched), resv: newReservationStore(), sched: sched,
	}
	svc.engine = newMigrationEngine(svc)
	return svc
}

func testNode(id uuid.UUID) sfuctl.NodeInfo {
	return sfuctl.NodeInfo{
		ID: id, Status: "ONLINE", Online: true, EnabledForScheduling: true,
		MaxUsers: 100, CurrentUsers: 10, WebRTCEndpoint: "ws://test/ws",
	}
}

// TestCreateJobSingleLockAndPreemption 同 user+guild 单 job 锁 + 高优先级抢占（docs 09 K.4/K.5）。
func TestCreateJobSingleLockAndPreemption(t *testing.T) {
	nodeA, nodeB := uuid.New(), uuid.New()
	svc := newTestService(t, []sfuctl.NodeInfo{testNode(nodeA), testNode(nodeB)})
	e := svc.engine
	guildID, userID, channelID := uuid.New(), uuid.New(), uuid.New()

	// 1. 低优先级（DRAIN）job
	drainJob, err := e.createJob(model.VoiceMigrationJob{
		Reason: model.MigrationReasonDrain, UserID: userID, GuildID: guildID,
		ChannelID: channelID, FromNodeID: nodeA,
	})
	if err != nil {
		t.Fatalf("创建 DRAIN job 失败: %v", err)
	}

	// 2. 同级重复创建 → 合并返回既有 job（单 job 锁）
	again, err := e.createJob(model.VoiceMigrationJob{
		Reason: model.MigrationReasonDrain, UserID: userID, GuildID: guildID,
		ChannelID: channelID, FromNodeID: nodeA,
	})
	if err != nil || again.ID != drainJob.ID {
		t.Fatalf("同级 job 应合并为既有 job：got %s want %s (err=%v)", again.ID, drainJob.ID, err)
	}

	// 3. 高优先级（DEATH）→ 抢占：旧 job 取消、新 job 生效
	deathJob, err := e.createJob(model.VoiceMigrationJob{
		Reason: model.MigrationReasonDeath, UserID: userID, GuildID: guildID,
		ChannelID: channelID, FromNodeID: nodeA,
	})
	if err != nil {
		t.Fatalf("创建 DEATH job 失败: %v", err)
	}
	if deathJob.ID == drainJob.ID {
		t.Fatal("高优先级应新建 job 抢占，而不是合并")
	}
	var old model.VoiceMigrationJob
	svc.db.First(&old, "id = ?", drainJob.ID)
	if old.State != model.MigrationStateCanceled {
		t.Fatalf("被抢占 job 应为 CANCELED，got %s", old.State)
	}

	// 4. 低优先级（OVERLOAD）来迟 → 被在途 DEATH job 吸收
	overload, err := e.createJob(model.VoiceMigrationJob{
		Reason: model.MigrationReasonOverload, UserID: userID, GuildID: guildID,
		ChannelID: channelID, FromNodeID: nodeA,
	})
	if err != nil || overload.ID != deathJob.ID {
		t.Fatalf("低优先级应合并进在途高优先级 job：got %s want %s", overload.ID, deathJob.ID)
	}
}

// TestMigrateNodeBatchConvergence 节点死亡批量迁移：同（源节点, 房间）共享
// batch_key；目标在附近候选上均匀分摊（migrateNode / pickLeastLoaded），
// sticky 仅影响调度排序，不强制全批单目标。
func TestMigrateNodeBatchConvergence(t *testing.T) {
	deadNode, aliveA, aliveB := uuid.New(), uuid.New(), uuid.New()
	// aliveA 上已有同房用户 → sticky 加分提高排序，但仍可能与 aliveB 分摊。
	nodes := []sfuctl.NodeInfo{testNode(deadNode), testNode(aliveA), testNode(aliveB)}
	svc := newTestService(t, nodes)
	guildID, channelID := uuid.New(), uuid.New()

	// 死节点上 3 个用户 + aliveA 上 1 个用户（sticky 信号）；anchor = aliveA（叶死不换根）。
	users := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for _, u := range users {
		svc.db.Create(&model.VoiceState{
			ID: uuid.New(), GuildID: guildID, UserID: u,
			ChannelID: &channelID, NodeID: &deadNode, RoomID: &channelID,
		})
	}
	sticky := uuid.New()
	svc.db.Create(&model.VoiceState{
		ID: uuid.New(), GuildID: guildID, UserID: sticky,
		ChannelID: &channelID, NodeID: &aliveA, RoomID: &channelID,
	})
	svc.db.Create(&model.VoiceAnchorLease{
		RoomID: channelID, GuildID: guildID, AnchorNodeID: aliveA,
		Epoch: 1, LeaseExpiresAt: time.Now().Add(time.Minute),
	})

	svc.engine.migrateNode(deadNode, model.MigrationReasonDeath)

	var jobs []model.VoiceMigrationJob
	svc.db.Find(&jobs, "guild_id = ? AND state <> ?", guildID, model.MigrationStateCanceled)
	if len(jobs) != len(users) {
		t.Fatalf("应为死节点上 %d 个用户各建一个 job，got %d", len(users), len(jobs))
	}
	wantKey := deadNode.String() + "@" + channelID.String()
	allowed := map[uuid.UUID]bool{aliveA: true, aliveB: true}
	for _, job := range jobs {
		if job.BatchKey != wantKey {
			t.Fatalf("batch_key 应为 %s，got %s", wantKey, job.BatchKey)
		}
		if job.ToNodeID == nil || !allowed[*job.ToNodeID] {
			t.Fatalf("批量迁移目标应落在存活节点 %s/%s，got %v", aliveA, aliveB, job.ToNodeID)
		}
		if job.Reason != model.MigrationReasonDeath || job.Priority != migrationPriority(model.MigrationReasonDeath) {
			t.Fatalf("reason/priority 不符：%s/%d", job.Reason, job.Priority)
		}
	}
}

// TestStepConnectTimeoutAdvances CONNECT 段超时自动推进到 CUTOVER
//（docs 09 M.4：客户端无响应不阻塞迁移）。
func TestStepConnectTimeoutAdvances(t *testing.T) {
	nodeA, nodeB := uuid.New(), uuid.New()
	svc := newTestService(t, []sfuctl.NodeInfo{testNode(nodeA), testNode(nodeB)})
	e := svc.engine

	past := time.Now().Add(-time.Second)
	job := model.VoiceMigrationJob{
		ID: uuid.New(), Reason: model.MigrationReasonDrain,
		Priority: migrationPriority(model.MigrationReasonDrain),
		UserID:   uuid.New(), GuildID: uuid.New(), ChannelID: uuid.New(),
		FromNodeID: nodeA, ToNodeID: &nodeB,
		State: model.MigrationStateConnect, StateDeadline: &past,
	}
	svc.db.Create(&job)

	e.step(job)
	var got model.VoiceMigrationJob
	svc.db.First(&got, "id = ?", job.ID)
	if got.State != model.MigrationStateCutover {
		t.Fatalf("CONNECT 超时应推进到 CUTOVER，got %s", got.State)
	}
	// 未到超时不推进。
	future := time.Now().Add(time.Minute)
	job2 := job
	job2.ID = uuid.New()
	job2.UserID = uuid.New()
	job2.StateDeadline = &future
	if err := svc.db.Create(&job2).Error; err != nil {
		t.Fatalf("创建 job2 失败: %v", err)
	}
	e.step(job2)
	var got2 model.VoiceMigrationJob
	if err := svc.db.First(&got2, "id = ?", job2.ID).Error; err != nil {
		t.Fatalf("读取 job2 失败: %v", err)
	}
	if got2.State != model.MigrationStateConnect {
		t.Fatalf("未超时不应推进，got %s", got2.State)
	}
}

// TestPickTargetRetrySwitchesNode 重试换目标：已尝试节点被排除（docs 09 K.3）。
func TestPickTargetRetrySwitchesNode(t *testing.T) {
	nodeFrom, nodeA, nodeB := uuid.New(), uuid.New(), uuid.New()
	svc := newTestService(t, []sfuctl.NodeInfo{testNode(nodeFrom), testNode(nodeA), testNode(nodeB)})
	e := svc.engine
	job := model.VoiceMigrationJob{
		ID: uuid.New(), UserID: uuid.New(), GuildID: uuid.New(), ChannelID: uuid.New(),
		FromNodeID: nodeFrom,
	}

	// 第一次：目标 ∈ {A, B}（源节点被硬过滤）。
	first, err := e.pickTarget(job)
	if err != nil {
		t.Fatalf("pickTarget 失败: %v", err)
	}
	if first != nodeA && first != nodeB {
		t.Fatalf("目标应在存活节点中，got %s", first)
	}
	// 标记第一次目标已尝试 → 重试必须换另一节点。
	job.TriedNodes = first.String()
	second, err := e.pickTarget(job)
	if err != nil {
		t.Fatalf("重试 pickTarget 失败: %v", err)
	}
	if second == first {
		t.Fatalf("重试应换目标，仍然选了 %s", first)
	}
	// 全部试过 → 无目标（排队重试，docs 09 J.4，禁止回大厅由上层保证）。
	job.TriedNodes = nodeA.String() + "," + nodeB.String()
	if _, err := e.pickTarget(job); err == nil {
		t.Fatal("全部节点已尝试时应返回错误进入排队重试")
	}

	// 预置目标（批量收敛）未尝试过时优先生效。
	job2 := model.VoiceMigrationJob{
		ID: uuid.New(), UserID: uuid.New(), GuildID: uuid.New(), ChannelID: uuid.New(),
		FromNodeID: nodeFrom, ToNodeID: &nodeB, BatchKey: "k",
	}
	got, err := e.pickTarget(job2)
	if err != nil || got != nodeB {
		t.Fatalf("预置 batch 目标应直接采用：got %v err=%v", got, err)
	}
	// 预置目标已尝试过 → 重新打分换目标。
	job2.TriedNodes = nodeB.String()
	got, err = e.pickTarget(job2)
	if err != nil || got != nodeA {
		t.Fatalf("预置目标已尝试时应换目标 %s：got %v err=%v", nodeA, got, err)
	}
}
