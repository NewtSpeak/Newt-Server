// partition.go 分区升级（docs 09 §3.3 补充信号 / 08 §7.2，M4 遗留接线）：
// 同一叶边短窗口内反复 EdgeDown（重发边集修复无效 ≈ 叶节点与树间网络分区）
// → 对该叶节点上本房用户触发 MIGRATE_LEAF（reason=PARTITION），走既有迁移状态机；
// 升级后对该边冷却，防止「迁移中继续 EdgeDown」造成的震荡重复升级。
package voice

import (
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

// 分区升级参数（docs 09 §3.3；窗口/次数为实现初值，可随压测调整）。
const (
	// edgeFlapWindow 观察窗口：窗口内同一边 EdgeDown 达到阈值即认定分区。
	edgeFlapWindow = 60 * time.Second
	// edgeFlapThreshold 窗口内 EdgeDown 次数阈值。
	edgeFlapThreshold = 3
	// edgeFlapCooldown 升级后的冷却时长：期间同一边不重复升级（防震荡）。
	edgeFlapCooldown = 5 * time.Minute
)

// edgeFlapTracker 每条级联边的 EdgeDown 抖动跟踪（内存，voice 编排持有）。
type edgeFlapTracker struct {
	window    time.Duration
	threshold int
	cooldown  time.Duration

	mu            sync.Mutex
	events        map[string][]time.Time
	cooldownUntil map[string]time.Time
}

func newEdgeFlapTracker() *edgeFlapTracker {
	return &edgeFlapTracker{
		window:        edgeFlapWindow,
		threshold:     edgeFlapThreshold,
		cooldown:      edgeFlapCooldown,
		events:        map[string][]time.Time{},
		cooldownUntil: map[string]time.Time{},
	}
}

// record 记录一次 EdgeDown，返回是否应升级为分区迁移。
// 升级即进入冷却：冷却期内同一边继续 EdgeDown 只计数不再触发。
func (t *edgeFlapTracker) record(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	kept := t.events[key][:0]
	for _, at := range t.events[key] {
		if now.Sub(at) <= t.window {
			kept = append(kept, at)
		}
	}
	kept = append(kept, now)
	t.events[key] = kept
	if until, cooling := t.cooldownUntil[key]; cooling && now.Before(until) {
		return false
	}
	if len(kept) < t.threshold {
		return false
	}
	t.cooldownUntil[key] = now.Add(t.cooldown)
	t.events[key] = nil // 升级后清零，冷却结束重新累计
	return true
}

// escalatePartition 对叶节点 childID 上本房用户批量创建 PARTITION 迁移
// （docs 09 K.4：优先级 死亡 > 分区 > Drain > 过载；同批预置目标收敛 J.5）。
// 必须已持有 s.mu。
func (s *Service) escalatePartition(guildID, roomID, childID uuid.UUID) {
	var states []model.VoiceState
	if err := s.db.Find(&states, "node_id = ? AND channel_id = ?", childID, roomID).Error; err != nil || len(states) == 0 {
		return
	}
	log.Printf("voice: 房间 %s 叶边 →%s 短窗内反复 EdgeDown，升级为分区迁移（%d 个会话，docs 09 §3.3）",
		roomID, childID, len(states))
	batchKey := childID.String() + "@" + roomID.String()
	var batchTarget *uuid.UUID
	if candidates, err := s.buildCandidates(guildID, roomID); err == nil {
		from := childID
		if result, ok := schedule(candidates, scheduleParams{
			Mode: ModeMigrateLeaf, UserID: states[0].UserID,
			FromNodeID: &from, Config: s.sched,
		}); ok {
			batchTarget = &result.Primary
		}
	}
	for _, vs := range states {
		_, err := s.engine.createJob(model.VoiceMigrationJob{
			Reason: model.MigrationReasonPartition, UserID: vs.UserID, GuildID: vs.GuildID,
			ChannelID: roomID, FromNodeID: childID, ToNodeID: batchTarget, BatchKey: batchKey,
		})
		if err != nil {
			log.Printf("voice: 创建分区迁移失败 user=%s: %v", vs.UserID, err)
		}
	}
}
