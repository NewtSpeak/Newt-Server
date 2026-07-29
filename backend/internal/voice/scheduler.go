package voice

import (
	"hash/fnv"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/sfuctl"
)

// ScheduleMode 调度模式（docs 10 §5）。
type ScheduleMode string

const (
	ModeJoin         ScheduleMode = "JOIN"
	ModeMigrateLeaf  ScheduleMode = "MIGRATE_LEAF"
	ModeMigrateBatch ScheduleMode = "MIGRATE_BATCH"
	ModeMigrateRoot  ScheduleMode = "MIGRATE_ROOT"
)

// scheduleWeights 归一化加权因子（docs 10 §4.1）。
type scheduleWeights struct {
	RTT      float64
	Region   float64
	Capacity float64
	Sticky   float64
	Tree     float64
}

// weightsFor 各 mode 默认权重（docs 10 §4.2）。
// MIGRATE_ROOT 的「选根」走 electAnchor；其上的用户迁移按 BATCH 权重打分。
func weightsFor(mode ScheduleMode) scheduleWeights {
	switch mode {
	case ModeMigrateLeaf:
		return scheduleWeights{RTT: 0.25, Region: 0.15, Capacity: 0.25, Sticky: 0.25, Tree: 0.10}
	case ModeMigrateBatch, ModeMigrateRoot:
		return scheduleWeights{RTT: 0.15, Region: 0.15, Capacity: 0.25, Sticky: 0.35, Tree: 0.10}
	default: // JOIN
		return scheduleWeights{RTT: 0.35, Region: 0.20, Capacity: 0.25, Sticky: 0.10, Tree: 0.10}
	}
}

// schedConfig 调度可配参数（docs 10 §10），当前使用内置默认值。
type schedConfig struct {
	ReserveRatio     float64 // 迁移预留比例，默认 5%
	SameRoomSoftCap  int     // 单节点同房人数软顶（T.4），达到后 sticky 降为 0
	StickySaturation int     // sticky 饱和人数：同房人数达到该值 S_sticky=1
	RTTClampMinMs    float64 // RTT 防作弊下限
	RTTClampMaxMs    float64 // RTT 防作弊上限
	RTTZeroScoreMs   float64 // 高于该 RTT 记 0 分（分段线性上界）
	JitterRatio      float64 // Join 随机微扰幅度（1–2%）
	RTTSampleTTL     time.Duration
	ReservationTTL   time.Duration
	// OverloadAutoMigrate 过载自动迁移开关（docs 09 I.3：默认关，本期仅留配置口；
	// 开启前需另行实装 I.4/I.5 的阈值检测、每轮 ≤15% 且 ≤50 人批量与冷却循环）。
	OverloadAutoMigrate bool
}

func defaultSchedConfig() schedConfig {
	return schedConfig{
		ReserveRatio:     0.05,
		SameRoomSoftCap:  80,
		StickySaturation: 8,
		RTTClampMinMs:    5,
		RTTClampMaxMs:    2000,
		RTTZeroScoreMs:   400,
		JitterRatio:      0.02,
		RTTSampleTTL:     60 * time.Second,
		ReservationTTL:   8 * time.Second, // 与 CONNECT 段超时对齐（docs 10 §8）
		// 过载自动迁移默认关（docs 09 I.3），系统管可开。
		OverloadAutoMigrate: false,
	}
}

// nodeCandidate 调度候选（节点快照 + 房间相关上下文）。
type nodeCandidate struct {
	Info sfuctl.NodeInfo
	// SameRoomUsers 该节点上同 logical_room 的用户数（S_sticky）。
	SameRoomUsers int
	// OnTree 该节点是否已在本房级联树上（S_tree）。
	OnTree bool
	// IsAnchor 该节点是否本房 anchor（S_tree 满分）。
	IsAnchor bool
	// Reserved 该节点当前活跃容量预留数。
	Reserved int
}

// scheduleParams 一次调度请求（docs 10 §2.1）。
type scheduleParams struct {
	Mode         ScheduleMode
	UserID       uuid.UUID
	ClientRegion string
	// RTTMs 有效 RTT 样本（已过 TTL 与 clamp），key 为 node_id。
	RTTMs map[uuid.UUID]float64
	// FromNodeID 迁移源节点（硬过滤排除）。
	FromNodeID *uuid.UUID
	// MustMatch 合规标签硬过滤（服策略 must_match，docs 10 §3.2）。
	MustMatch map[string]string
	Config    schedConfig
	// Jitter 返回 [0,1) 随机数；nil 表示关闭微扰（迁移必须关闭，docs 10 §4.3）。
	Jitter func() float64
}

// scheduleResult 输出 primary + fallbacks（仅 Server 内部使用，不下发客户端，docs 10 X.3）。
type scheduleResult struct {
	Primary   uuid.UUID
	Fallbacks []uuid.UUID
}

// migrationReserve 每节点迁移预留名额：max(1, floor(max_users * ratio))（docs 10 §3.1）。
func migrationReserve(maxUsers int, ratio float64) int {
	reserve := int(math.Floor(float64(maxUsers) * ratio))
	if reserve < 1 {
		reserve = 1
	}
	return reserve
}

// passHardFilter 硬过滤（docs 10 §3）：不满足直接出局，不进打分。
// 候选集合本身应来自 Guild 节点池（sfuctl.Dir().PoolNodes），池外过滤由调用方保证。
func passHardFilter(c nodeCandidate, p scheduleParams) bool {
	info := c.Info
	// 2. ONLINE 且控制通道健康、启用调度。
	if !info.Online || info.Status != "ONLINE" || !info.EnabledForScheduling {
		return false
	}
	// 3. DRAINING 硬拒。
	if info.Draining {
		return false
	}
	// 4. 迁移排除源节点。
	if p.FromNodeID != nil && info.ID == *p.FromNodeID {
		return false
	}
	// 5. 合规标签 must_match 硬拒。
	for key, want := range p.MustMatch {
		if info.Labels[key] != want {
			return false
		}
	}
	// 6. 容量：Join 不可吃 5% 迁移预留；迁移可用全部容量。
	if info.MaxUsers <= 0 {
		return false
	}
	used := info.CurrentUsers + c.Reserved
	if p.Mode == ModeJoin {
		reserve := migrationReserve(info.MaxUsers, p.Config.ReserveRatio)
		return used+1 <= info.MaxUsers-reserve
	}
	return used+1 <= info.MaxUsers
}

// scoreRTT 无样本 = 0.5（docs 10 R.4）；有样本按 [clampMin, zeroScoreMs] 分段线性。
func scoreRTT(rtt float64, ok bool, cfg schedConfig) float64 {
	if !ok {
		return 0.5
	}
	rtt = clamp(rtt, cfg.RTTClampMinMs, cfg.RTTClampMaxMs)
	if rtt >= cfg.RTTZeroScoreMs {
		return 0
	}
	return 1 - (rtt-cfg.RTTClampMinMs)/(cfg.RTTZeroScoreMs-cfg.RTTClampMinMs)
}

// scoreRegion 完全匹配 1；同大区（'-' 前缀相同，如 ap-southeast / ap-northeast）0.5；否则 0。
// 客户端未知 region 时给中性 0.5。
func scoreRegion(clientRegion, nodeRegion string) float64 {
	if clientRegion == "" || nodeRegion == "" {
		return 0.5
	}
	if clientRegion == nodeRegion {
		return 1
	}
	if regionPrefix(clientRegion) == regionPrefix(nodeRegion) {
		return 0.5
	}
	return 0
}

func regionPrefix(region string) string {
	if idx := strings.IndexByte(region, '-'); idx > 0 {
		return region[:idx]
	}
	return region
}

// scoreCapacity 综合剩余用户余量与 CPU 余量；出口带宽缺少容量上限数据，暂不计入（docs 10 R.1 注记）。
func scoreCapacity(info sfuctl.NodeInfo, reserved int) float64 {
	userSlack := 1 - float64(info.CurrentUsers+reserved)/float64(info.MaxUsers)
	cpuSlack := 1 - info.CPUPercent/100
	return 0.7*clamp(userSlack, 0, 1) + 0.3*clamp(cpuSlack, 0, 1)
}

// scoreSticky 同房人数越多越高，saturation 处饱和；达到软顶后降为 0（docs 10 T.4 降分非硬拒）。
func scoreSticky(sameRoomUsers int, cfg schedConfig) float64 {
	if cfg.SameRoomSoftCap > 0 && sameRoomUsers >= cfg.SameRoomSoftCap {
		return 0
	}
	if cfg.StickySaturation <= 0 {
		return 0
	}
	return clamp(float64(sameRoomUsers)/float64(cfg.StickySaturation), 0, 1)
}

// scoreTree anchor 满分，已在树上次之（少建边，docs 10 T.3）。
func scoreTree(c nodeCandidate) float64 {
	if c.IsAnchor {
		return 1
	}
	if c.OnTree {
		return 0.8
	}
	return 0
}

// scoreNode 归一化加权总分（docs 10 §4.1）。
func scoreNode(c nodeCandidate, p scheduleParams) float64 {
	w := weightsFor(p.Mode)
	rtt, ok := p.RTTMs[c.Info.ID]
	score := w.RTT*scoreRTT(rtt, ok, p.Config) +
		w.Region*scoreRegion(p.ClientRegion, c.Info.Region) +
		w.Capacity*scoreCapacity(c.Info, c.Reserved) +
		w.Sticky*scoreSticky(c.SameRoomUsers, p.Config) +
		w.Tree*scoreTree(c)
	// Join 允许 1–2% 随机微扰防羊群；迁移关闭保证可复现（docs 10 §4.3）。
	if p.Mode == ModeJoin && p.Jitter != nil {
		score += p.Jitter() * p.Config.JitterRatio
	}
	return score
}

// stableTieHash 同分时用 hash(user_id, node_id) 稳定打散（docs 10 V.2）。
func stableTieHash(userID, nodeID uuid.UUID) uint64 {
	h := fnv.New64a()
	h.Write(userID[:])
	h.Write(nodeID[:])
	return h.Sum64()
}

// schedule 硬过滤 → 打分 → 排序，输出 primary + fallbacks；无合格候选返回 ok=false。
func schedule(candidates []nodeCandidate, p scheduleParams) (scheduleResult, bool) {
	type scored struct {
		id    uuid.UUID
		score float64
		tie   uint64
	}
	passed := make([]scored, 0, len(candidates))
	for _, c := range candidates {
		if !passHardFilter(c, p) {
			continue
		}
		passed = append(passed, scored{id: c.Info.ID, score: scoreNode(c, p), tie: stableTieHash(p.UserID, c.Info.ID)})
	}
	if len(passed) == 0 {
		return scheduleResult{}, false
	}
	sort.Slice(passed, func(i, j int) bool {
		if math.Abs(passed[i].score-passed[j].score) > 1e-9 {
			return passed[i].score > passed[j].score
		}
		return passed[i].tie < passed[j].tie
	})
	result := scheduleResult{Primary: passed[0].id}
	for _, s := range passed[1:] {
		result.Fallbacks = append(result.Fallbacks, s.id)
	}
	return result, true
}

// defaultJitter 生产用随机微扰源。
func defaultJitter() float64 { return rand.Float64() }

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------------------------------------------------------------------------
// RTT 样本仓（内存，TTL 60s，docs 10 §7）。样本很短命，无需落库。
// ---------------------------------------------------------------------------

type rttSample struct {
	RTTMs      float64
	MeasuredAt time.Time
}

type rttStore struct {
	mu     sync.Mutex
	byUser map[uuid.UUID]map[uuid.UUID]rttSample
	cfg    schedConfig
}

func newRTTStore(cfg schedConfig) *rttStore {
	return &rttStore{byUser: make(map[uuid.UUID]map[uuid.UUID]rttSample), cfg: cfg}
}

// Report 记录一条样本；clamp 到 [5, 2000]ms 防作弊刷分（docs 10 S.3）。
func (s *rttStore) Report(userID, nodeID uuid.UUID, rttMs float64, now time.Time) {
	rttMs = clamp(rttMs, s.cfg.RTTClampMinMs, s.cfg.RTTClampMaxMs)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byUser[userID] == nil {
		s.byUser[userID] = make(map[uuid.UUID]rttSample)
	}
	s.byUser[userID][nodeID] = rttSample{RTTMs: rttMs, MeasuredAt: now}
}

// Samples 返回某用户未过期的样本；顺带清理过期项。
func (s *rttStore) Samples(userID uuid.UUID, now time.Time) map[uuid.UUID]float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[uuid.UUID]float64)
	for nodeID, sample := range s.byUser[userID] {
		if now.Sub(sample.MeasuredAt) > s.cfg.RTTSampleTTL {
			delete(s.byUser[userID], nodeID)
			continue
		}
		result[nodeID] = sample.RTTMs
	}
	return result
}

// ---------------------------------------------------------------------------
// 容量预留（内存，TTL 数秒自动释放，docs 10 §8）。
// ---------------------------------------------------------------------------

type reservation struct {
	NodeID    uuid.UUID
	ExpiresAt time.Time
}

type reservationStore struct {
	mu    sync.Mutex
	items map[uuid.UUID]reservation
}

func newReservationStore() *reservationStore {
	return &reservationStore{items: make(map[uuid.UUID]reservation)}
}

// Reserve 预留一个名额，返回 reservation_id；TTL 到期自动失效。
func (s *reservationStore) Reserve(nodeID uuid.UUID, ttl time.Duration) uuid.UUID {
	id := uuid.New()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[id] = reservation{NodeID: nodeID, ExpiresAt: time.Now().Add(ttl)}
	return id
}

// Release 主动释放（会话建立后或失败时）。
func (s *reservationStore) Release(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
}

// ActiveByNode 各节点当前活跃预留数；顺带清理过期项。
func (s *reservationStore) ActiveByNode() map[uuid.UUID]int {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := make(map[uuid.UUID]int)
	for id, r := range s.items {
		if now.After(r.ExpiresAt) {
			delete(s.items, id)
			continue
		}
		counts[r.NodeID]++
	}
	return counts
}
