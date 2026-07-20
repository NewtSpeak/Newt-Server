package stage

import (
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
)

// 舞台与屏幕共享的核心参数（docs 11 / docs 14 定稿）。
const (
	// CapacityWindow >50 强制 STAGE 与容量禁说阈值（docs 11 Z.1：最多 50 人不受 CAPACITY_QUEUE 禁说）。
	CapacityWindow = 50
	// HardMaxSpeakers 台上硬顶（docs 11 AA.1）。
	HardMaxSpeakers = 50
	// DefaultMaxSpeakers 台上默认软限（docs 11 AA.1）。
	DefaultMaxSpeakers = 20
	// MaxQueueLength 申请队列上限（docs 11 AB.7）。
	MaxQueueLength = 100
	// QueueEntryTTL 申请 30 分钟无处理过期（docs 11 AC.2）。
	QueueEntryTTL = 30 * time.Minute
	// DisconnectGrace 断线保留席位/队位/屏幕配额时长（docs 11 AC.3、docs 14 BB.4）。
	DisconnectGrace = 60 * time.Second
	// ReservationTTL 屏幕共享 RESERVED 占坑确认超时（docs 14 AZ.4）。
	ReservationTTL = 60 * time.Second
	// DefaultGuildScreens 新服屏幕并发基准默认值（docs 14 AY.7）。
	DefaultGuildScreens = 3
	// DefaultChannelScreens 频道屏幕并发默认值（docs 14 AY.2）。
	DefaultChannelScreens = 2
)

// DiffCapacityMutes 按进房顺序重算容量禁说集合（docs 11 §3.2）。
//   - orderedUsers：频道内 connected 用户，按 JoinedAt 升序（进房顺序）。
//   - speakers：当前持有 SPEAKER 席位的用户（被抱上者解除有效禁说，docs 11 AD.1.4）。
//   - muted：当前已被容量禁说的用户。
//   - window：不受禁说的窗口人数（正常取 CapacityWindow=50）。
//
// 语义：进房序前 window 名以及 SPEAKER 免疫；其余需禁说。
// 由于窗口按进房序滑动，有人离开时最早被禁说者（= 被禁说者中进房最早者）自然滑入窗口，
// 即实现了「FIFO：最早被容量禁说者优先解除」（docs 11 Z.5）。
func DiffCapacityMutes(orderedUsers []uuid.UUID, speakers, muted map[uuid.UUID]bool, window int) (toMute, toUnmute []uuid.UUID) {
	present := make(map[uuid.UUID]bool, len(orderedUsers))
	for i, id := range orderedUsers {
		present[id] = true
		exempt := i < window || speakers[id]
		switch {
		case exempt && muted[id]:
			toUnmute = append(toUnmute, id)
		case !exempt && !muted[id]:
			toMute = append(toMute, id)
		}
	}
	// 已离开频道的被禁说者直接解除标记（其 VoiceState 已不在频道内）。
	var absent []uuid.UUID
	for id := range muted {
		if !present[id] {
			absent = append(absent, id)
		}
	}
	sort.Slice(absent, func(i, j int) bool { return absent[i].String() < absent[j].String() })
	toUnmute = append(toUnmute, absent...)
	return toMute, toUnmute
}

// Seat 一个 SPEAKER 席位的裁剪视图。
type Seat struct {
	UserID uuid.UUID
	Since  time.Time // 席位授予时间；FREE→STAGE 切换时以进房时间近似「可说话更久」（docs 11 Y.3，近似实现）
}

// TrimSpeakers FREE→STAGE 或 max_speakers 下调时的留台裁剪：
// 「可说话状态更久者优先留台」，即 Since 更早者优先；超出部分降为 AUDIENCE（docs 11 Y.3）。
func TrimSpeakers(seats []Seat, maxSpeakers int) (kept, demoted []Seat) {
	if maxSpeakers < 0 {
		maxSpeakers = 0
	}
	sorted := make([]Seat, len(seats))
	copy(sorted, seats)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Since.Before(sorted[j].Since) })
	if len(sorted) <= maxSpeakers {
		return sorted, nil
	}
	return sorted[:maxSpeakers], sorted[maxSpeakers:]
}

// ApplyInput 申请上麦的裁决输入（docs 11 AB.2/AB.3/AB.7/AB.8）。
type ApplyInput struct {
	Mode           string // 频道当前模式
	RequestEnabled bool   // request_to_speak_enabled
	InChannel      bool   // 是否已在该语音频道内
	IsSpeaker      bool   // 已是 SPEAKER 不可申请
	Restricted     bool   // Restriction 禁说者不可申请上台
	AlreadyQueued  bool   // 已在队列中（幂等）
	QueueLength    int    // 当前队列长度
}

// DecideApply 申请上麦裁决。errCode 为空表示允许；idempotent=true 表示重复申请保持原位（docs 11 AB.8）。
func DecideApply(in ApplyInput) (errCode string, idempotent bool) {
	if in.Mode != "STAGE" {
		return "STAGE_NOT_ACTIVE", false
	}
	if !in.InChannel {
		return "NOT_IN_VOICE", false
	}
	if in.IsSpeaker {
		return "STAGE_ALREADY_SPEAKER", false
	}
	if in.Restricted {
		return "RESTRICTED", false
	}
	if in.AlreadyQueued {
		return "", true
	}
	if !in.RequestEnabled {
		return "STAGE_REQUEST_DISABLED", false
	}
	if in.QueueLength >= MaxQueueLength {
		return "STAGE_QUEUE_FULL", false
	}
	return "", false
}

// BringUpInput 抱上麦的裁决输入（docs 11 AB.4 / AA.3）。
type BringUpInput struct {
	Mode           string // 频道当前模式
	InChannel      bool   // 目标是否在频道内
	AlreadySpeaker bool   // 已在台上（幂等）
	Restricted     bool   // 目标被 Restriction 禁说，不可上台（docs 11 AD.1）
	SpeakerCount   int    // 当前台上人数
	MaxSpeakers    int    // 频道 max_speakers
}

// DecideBringUp 抱上裁决。返回空串表示允许；STAGE_ALREADY_SPEAKER 表示幂等成功；
// 台上已满返回 STAGE_FULL（满额拒绝，须先抱下他人，docs 11 AA.3）。
func DecideBringUp(in BringUpInput) string {
	if in.Mode != "STAGE" {
		return "STAGE_NOT_ACTIVE"
	}
	if !in.InChannel {
		return "NOT_IN_VOICE"
	}
	if in.AlreadySpeaker {
		return "STAGE_ALREADY_SPEAKER"
	}
	if in.Restricted {
		return "RESTRICTED"
	}
	if in.SpeakerCount >= in.MaxSpeakers {
		return "STAGE_FULL"
	}
	return ""
}

// NodeLoad 动态降额所需的节点负载切片（由 sfuctl.NodeInfo 映射，保持纯函数可测）。
type NodeLoad struct {
	CPUPercent   float64
	MaxUsers     int
	CurrentUsers int
	ScreenTracks int
}

// DynamicScreenCap 按节点池负载聚合计算动态屏幕配额上限（docs 14 AY.4–AY.6）。
// 负载评分取「平均 CPU 占比」与「用户占用率」的较大者，走简单分段函数降额：
//
//	score < 0.60      → 不降额（factor 1.00）
//	0.60 ≤ score <0.75 → factor 0.75
//	0.75 ≤ score <0.90 → factor 0.50
//	score ≥ 0.90      → factor 0.25
//
// 注意：各分段阈值与系数为初值，待压测标定（docs 14 BD.2）。
// 结果永不高于基准，且至少为 1（负载恢复后随评分回落自然抬升，不超过基准）。
func DynamicScreenCap(nodes []NodeLoad, base int) int {
	if base <= 0 {
		return 0
	}
	if len(nodes) == 0 {
		// 无节点负载数据（如未接入真实 SFU）时不降额。
		return base
	}
	var cpuSum float64
	var users, capacity int
	for _, node := range nodes {
		cpuSum += node.CPUPercent
		users += node.CurrentUsers
		capacity += node.MaxUsers
	}
	score := cpuSum / float64(len(nodes)) / 100
	if capacity > 0 {
		if occupancy := float64(users) / float64(capacity); occupancy > score {
			score = occupancy
		}
	}
	factor := 1.0
	switch {
	case score < 0.60:
		factor = 1.0
	case score < 0.75:
		factor = 0.75
	case score < 0.90:
		factor = 0.50
	default:
		factor = 0.25
	}
	result := int(math.Floor(float64(base) * factor))
	if result < 1 {
		result = 1
	}
	if result > base {
		result = base
	}
	return result
}

// EffectiveGuildCap 服级有效上限 = min(基准, dynamic_cap)；动态关闭时等于基准（docs 14 AY §3.1）。
func EffectiveGuildCap(base int, dynamicEnabled bool, dynamicCap int) int {
	if !dynamicEnabled {
		return base
	}
	return min(base, dynamicCap)
}

// RemainingScreens 实际还允许新开的屏幕路数 = min(频道剩余, 服有效剩余)，不小于 0（docs 14 AY.2）。
func RemainingScreens(channelUsed, channelCap, guildUsed, guildEffective int) int {
	remaining := min(channelCap-channelUsed, guildEffective-guildUsed)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// 质量档位（docs 14 BA.1）。
var qualityRank = map[string]int{"480p": 1, "720p": 2, "1080p": 3}

// QualityAllowed 判断请求档位是否不超过允许上限档。未知档位一律拒绝。
func QualityAllowed(requested, maxAllowed string) bool {
	req, ok1 := qualityRank[requested]
	limit, ok2 := qualityRank[maxAllowed]
	return ok1 && ok2 && req <= limit
}
