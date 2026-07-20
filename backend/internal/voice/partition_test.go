package voice

// 分区升级单测（docs 09 §3.3 / 08 §7.2）：同一叶边短窗口内反复 EdgeDown →
// 升级 MIGRATE_LEAF（reason=PARTITION）；升级后冷却防震荡。

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
)

// TestEdgeFlapTracker 窗口计数、阈值触发、冷却抑制与窗口滑出。
func TestEdgeFlapTracker(t *testing.T) {
	tr := newEdgeFlapTracker()
	key := "room/p>c"
	t0 := time.Now()

	// 窗口内前 2 次不触发，第 3 次触发。
	if tr.record(key, t0) {
		t.Fatal("第 1 次 EdgeDown 不应升级")
	}
	if tr.record(key, t0.Add(10*time.Second)) {
		t.Fatal("第 2 次 EdgeDown 不应升级")
	}
	if !tr.record(key, t0.Add(20*time.Second)) {
		t.Fatal("60s 内第 3 次 EdgeDown 应升级为分区迁移")
	}
	// 冷却期内继续断边：不重复升级（防震荡）。
	for i := 0; i < 5; i++ {
		if tr.record(key, t0.Add(time.Duration(30+i)*time.Second)) {
			t.Fatal("冷却期内不应重复升级")
		}
	}
	// 冷却结束后重新累计：需再满 3 次才触发。
	t1 := t0.Add(edgeFlapCooldown + 30*time.Second)
	if tr.record(key, t1) || tr.record(key, t1.Add(time.Second)) {
		t.Fatal("冷却结束后应重新累计，不足阈值不触发")
	}
	if !tr.record(key, t1.Add(2*time.Second)) {
		t.Fatal("冷却结束后再满阈值应再次升级")
	}

	// 窗口滑出：跨窗口的零散断边不触发。
	slow := newEdgeFlapTracker()
	if slow.record(key, t0) {
		t.Fatal("不应触发")
	}
	if slow.record(key, t0.Add(70*time.Second)) {
		t.Fatal("窗口外旧事件应滑出，不应触发")
	}
	if slow.record(key, t0.Add(140*time.Second)) {
		t.Fatal("零散断边不应触发")
	}

	// 不同边独立计数。
	other := "room/p>c2"
	if tr.record(other, t1) || tr.record(other, t1.Add(time.Second)) {
		t.Fatal("其他边独立计数，不足阈值不触发")
	}
}

// TestEscalatePartitionCreatesLeafJobs 升级后对叶节点用户创建 PARTITION 迁移
// （需要 TEST_DATABASE_URL）。
func TestEscalatePartitionCreatesLeafJobs(t *testing.T) {
	leafNode, anchorNode := uuid.New(), uuid.New()
	svc := newTestService(t, []sfuctl.NodeInfo{testNode(leafNode), testNode(anchorNode)})
	guildID, roomID := uuid.New(), uuid.New()

	// 叶节点 2 个用户 + anchor 上 1 个用户（不应被迁）。
	leafUsers := []uuid.UUID{uuid.New(), uuid.New()}
	for _, u := range leafUsers {
		svc.db.Create(&model.VoiceState{
			ID: uuid.New(), GuildID: guildID, UserID: u,
			ChannelID: &roomID, NodeID: &leafNode, RoomID: &roomID,
		})
	}
	anchorUser := uuid.New()
	svc.db.Create(&model.VoiceState{
		ID: uuid.New(), GuildID: guildID, UserID: anchorUser,
		ChannelID: &roomID, NodeID: &anchorNode, RoomID: &roomID,
	})

	svc.escalatePartition(guildID, roomID, leafNode)

	var jobs []model.VoiceMigrationJob
	svc.db.Find(&jobs, "guild_id = ?", guildID)
	if len(jobs) != len(leafUsers) {
		t.Fatalf("应为叶节点 %d 个用户各建一个 job，got %d", len(leafUsers), len(jobs))
	}
	for _, job := range jobs {
		if job.Reason != model.MigrationReasonPartition {
			t.Fatalf("reason 应为 PARTITION，got %s", job.Reason)
		}
		if job.Priority != migrationPriority(model.MigrationReasonPartition) {
			t.Fatalf("priority 不符: %d", job.Priority)
		}
		if job.FromNodeID != leafNode {
			t.Fatalf("from_node 应为叶节点，got %s", job.FromNodeID)
		}
		if job.UserID == anchorUser {
			t.Fatal("anchor 节点用户不应被分区迁移")
		}
		// 同批预置目标收敛到存活 anchor 节点（sticky 加分）。
		if job.ToNodeID == nil || *job.ToNodeID != anchorNode {
			t.Fatalf("批量目标应收敛到 %s，got %v", anchorNode, job.ToNodeID)
		}
	}
}
