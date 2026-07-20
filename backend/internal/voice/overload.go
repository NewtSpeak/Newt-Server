// overload.go 过载自动迁移循环（docs 09 §3.4 I.3–I.5）：
// 节点 CPU / 出口带宽 / 并发占用「超阈值持续 T 秒」→ 触发 MIGRATE_BATCH，
// 每轮 ≤ 该节点会话 15% 且 ≤ 50 人（取更严），轮后冷却再评估防雪崩打满邻居。
// 自动开关默认关（I.3），仅系统管/部署配置可开；手动迁移路径不受此影响。
//
// 迁移对象挑选遵循过载公平性（docs 09 §8）：优先迁 听众/非台上（N.1，
// 舞台 SPEAKER 席位可查，接 model.StageSpeaker）；同优先级偏好进房时间短（N.2；
// N.2 的「当前未在说话」信号在 Server 侧无低成本来源，留待后续接 speaking 上报）。
package voice

import (
	"log"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
)

// overloadConfig 过载检测与批量参数（docs 09 I.3–I.5，阈值可配）。
type overloadConfig struct {
	// Enabled 自动迁移开关（I.3 默认关）。
	Enabled bool
	// CPUPctThreshold CPU 占用阈值（%）。
	CPUPctThreshold float64
	// BandwidthOutMbpsThreshold 出口带宽阈值（Mbps，节点心跳自报用量）；
	// ≤0 表示不启用该维度（部署未知节点出口容量时避免误判）。
	BandwidthOutMbpsThreshold float64
	// UserRatioThreshold 并发用户占 max_users 的比例阈值。
	UserRatioThreshold float64
	// Sustain 超阈值须持续的时长 T（I.3）。
	Sustain time.Duration
	// Cooldown 每轮批量后的冷却时长（I.5：轮后冷却再评估）。
	Cooldown time.Duration
	// BatchRatio / BatchMax 每轮批量上限：≤ 会话数×ratio 且 ≤ max（取更严，I.5）。
	BatchRatio float64
	BatchMax   int
	// EvalInterval 评估周期。
	EvalInterval time.Duration
}

func defaultOverloadConfig() overloadConfig {
	return overloadConfig{
		Enabled:                   false, // I.3 定稿默认关
		CPUPctThreshold:           85,
		BandwidthOutMbpsThreshold: 0, // 默认不启用带宽维度（无出口容量基线）
		UserRatioThreshold:        0.90,
		Sustain:                   30 * time.Second,
		Cooldown:                  60 * time.Second,
		BatchRatio:                0.15,
		BatchMax:                  50,
		EvalInterval:              5 * time.Second,
	}
}

// overloadConfigFromEnv 部署配置（internal/config 环境变量）→ overloadConfig；
// 批量上限（≤15% 且 ≤50）为 docs 09 I.5 定稿值，不开放配置。
func overloadConfigFromEnv(cfg config.Config) overloadConfig {
	out := defaultOverloadConfig()
	out.Enabled = cfg.VoiceOverloadAutoMigrate
	if cfg.VoiceOverloadCPUPct > 0 {
		out.CPUPctThreshold = cfg.VoiceOverloadCPUPct
	}
	out.BandwidthOutMbpsThreshold = cfg.VoiceOverloadBandwidthMbps
	if cfg.VoiceOverloadUserRatio > 0 {
		out.UserRatioThreshold = cfg.VoiceOverloadUserRatio
	}
	if cfg.VoiceOverloadSustain > 0 {
		out.Sustain = cfg.VoiceOverloadSustain
	}
	if cfg.VoiceOverloadCooldown > 0 {
		out.Cooldown = cfg.VoiceOverloadCooldown
	}
	return out
}

// nodeOverloaded 单节点是否超阈值（任一维度命中即算，docs 09 I.3）。纯函数。
func nodeOverloaded(info sfuctl.NodeInfo, cfg overloadConfig) bool {
	if !info.Online || info.Draining {
		return false
	}
	if cfg.CPUPctThreshold > 0 && info.CPUPercent >= cfg.CPUPctThreshold {
		return true
	}
	if cfg.BandwidthOutMbpsThreshold > 0 && info.BandwidthOutMbps >= cfg.BandwidthOutMbpsThreshold {
		return true
	}
	if cfg.UserRatioThreshold > 0 && info.MaxUsers > 0 &&
		float64(info.CurrentUsers) >= cfg.UserRatioThreshold*float64(info.MaxUsers) {
		return true
	}
	return false
}

// overloadBatchSize 每轮批量 = min(floor(sessions×ratio), max)（I.5 取更严）。
// 会话数 >0 但 15% 取整为 0 时放行 1 人：否则小节点过载永不缓解（实现注记，
// 上限语义不变——单人已是最小迁移单位）。纯函数。
func overloadBatchSize(sessions int, cfg overloadConfig) int {
	if sessions <= 0 {
		return 0
	}
	batch := int(math.Floor(float64(sessions) * cfg.BatchRatio))
	if batch > cfg.BatchMax {
		batch = cfg.BatchMax
	}
	if batch < 1 {
		batch = 1
	}
	return batch
}

// orderOverloadVictims 迁移对象排序（docs 09 N.1/N.2）：
// 非台上（听众）优先于台上 SPEAKER；同级内进房时间短者优先。纯函数。
func orderOverloadVictims(states []model.VoiceState, onStage map[uuid.UUID]bool) []model.VoiceState {
	sorted := make([]model.VoiceState, len(states))
	copy(sorted, states)
	joined := func(vs model.VoiceState) time.Time {
		if vs.JoinedAt != nil {
			return *vs.JoinedAt
		}
		return time.Time{} // 无进房时间视为最早（排最后迁）
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		si, sj := onStage[sorted[i].UserID], onStage[sorted[j].UserID]
		if si != sj {
			return !si // 非台上在前
		}
		return joined(sorted[i]).After(joined(sorted[j])) // 进房时间短（更晚进）在前
	})
	return sorted
}

// ---------------------------------------------------------------------------
// 检测器（纯内存状态，指标源可注入，便于单测）
// ---------------------------------------------------------------------------

// overloadDetector 跟踪每个节点的「超阈值起始时刻」与「冷却截止时刻」。
type overloadDetector struct {
	cfg overloadConfig
	// overSince 节点连续超阈值的起始时刻；恢复正常即删除。
	overSince map[uuid.UUID]time.Time
	// cooldownUntil 该节点下一次允许触发批量的时刻（I.5 轮后冷却）。
	cooldownUntil map[uuid.UUID]time.Time
}

func newOverloadDetector(cfg overloadConfig) *overloadDetector {
	return &overloadDetector{
		cfg:           cfg,
		overSince:     map[uuid.UUID]time.Time{},
		cooldownUntil: map[uuid.UUID]time.Time{},
	}
}

// evaluate 用一批节点快照推进检测状态，返回本轮应触发批量迁移的节点。
// 触发条件：开关开 && 超阈值持续 ≥ Sustain && 不在冷却期；触发即进入冷却。
func (d *overloadDetector) evaluate(nodes []sfuctl.NodeInfo, now time.Time) []uuid.UUID {
	if !d.cfg.Enabled {
		return nil
	}
	seen := map[uuid.UUID]bool{}
	var fire []uuid.UUID
	for _, info := range nodes {
		seen[info.ID] = true
		if !nodeOverloaded(info, d.cfg) {
			delete(d.overSince, info.ID)
			continue
		}
		since, ok := d.overSince[info.ID]
		if !ok {
			d.overSince[info.ID] = now
			continue
		}
		if now.Sub(since) < d.cfg.Sustain {
			continue
		}
		if until, cooling := d.cooldownUntil[info.ID]; cooling && now.Before(until) {
			continue
		}
		d.cooldownUntil[info.ID] = now.Add(d.cfg.Cooldown)
		fire = append(fire, info.ID)
	}
	// 快照里消失的节点（下线/摘除）清理跟踪状态，防泄漏。
	for nodeID := range d.overSince {
		if !seen[nodeID] {
			delete(d.overSince, nodeID)
		}
	}
	for nodeID := range d.cooldownUntil {
		if !seen[nodeID] {
			delete(d.cooldownUntil, nodeID)
		}
	}
	return fire
}

// ---------------------------------------------------------------------------
// Service 集成
// ---------------------------------------------------------------------------

// overloadLoop 周期评估全部节点并触发批量迁出。指标源与检测器在 Service 构造时
// 注入（生产 = sfuctl.Dir().AllNodes()；单测替换假快照）。
func (s *Service) overloadLoop() {
	ticker := time.NewTicker(s.overload.cfg.EvalInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.overloadTick(time.Now())
	}
}

// overloadTick 单轮评估（可直接驱动，便于测试）。
func (s *Service) overloadTick(now time.Time) {
	nodes, err := s.overloadNodes()
	if err != nil {
		return
	}
	for _, nodeID := range s.overload.evaluate(nodes, now) {
		s.migrateOverloadedNode(nodeID)
	}
}

// migrateOverloadedNode 对过载节点触发一轮 MIGRATE_BATCH（docs 09 I.5 / 10 U.2）：
// 挑选 ≤15% 且 ≤50 的迁移对象（听众/非台上优先，N.1），按房间分批定 batch_target。
func (s *Service) migrateOverloadedNode(nodeID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var states []model.VoiceState
	if err := s.db.Find(&states, "node_id = ? AND channel_id IS NOT NULL", nodeID).Error; err != nil {
		return
	}
	if len(states) == 0 {
		return
	}
	batch := overloadBatchSize(len(states), s.overload.cfg)

	// 台上 SPEAKER 集合（docs 09 N.1：尽量不迁台上发言者）。
	onStage := map[uuid.UUID]bool{}
	var speakers []model.StageSpeaker
	if err := s.db.Find(&speakers, "guild_id IN (SELECT DISTINCT guild_id FROM voice_states WHERE node_id = ?)", nodeID).Error; err == nil {
		for _, sp := range speakers {
			onStage[sp.UserID] = true
		}
	}
	victims := orderOverloadVictims(states, onStage)[:batch]
	log.Printf("voice: 节点 %s 过载，触发批量迁出 %d/%d 个会话（≤%.0f%% 且 ≤%d，docs 09 I.5）",
		nodeID, len(victims), len(states), s.overload.cfg.BatchRatio*100, s.overload.cfg.BatchMax)

	// 按（源节点, 房间）分批：先为批代表打分定 batch_target（docs 10 U.2 同房收敛）。
	byRoom := map[uuid.UUID][]model.VoiceState{}
	for _, vs := range victims {
		byRoom[*vs.ChannelID] = append(byRoom[*vs.ChannelID], vs)
	}
	for roomID, members := range byRoom {
		guildID := members[0].GuildID
		batchKey := nodeID.String() + "@" + roomID.String()
		var batchTarget *uuid.UUID
		if candidates, err := s.buildCandidates(guildID, roomID); err == nil {
			from := nodeID
			if result, ok := schedule(candidates, scheduleParams{
				Mode: ModeMigrateBatch, UserID: members[0].UserID,
				FromNodeID: &from, Config: s.sched,
			}); ok {
				batchTarget = &result.Primary
			}
		}
		for _, vs := range members {
			_, err := s.engine.createJob(model.VoiceMigrationJob{
				Reason: model.MigrationReasonOverload, UserID: vs.UserID, GuildID: vs.GuildID,
				ChannelID: roomID, FromNodeID: nodeID, ToNodeID: batchTarget, BatchKey: batchKey,
			})
			if err != nil {
				log.Printf("voice: 创建过载迁移失败 user=%s: %v", vs.UserID, err)
			}
		}
	}
}
