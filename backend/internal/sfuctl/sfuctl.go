// Package sfuctl 是「SFU 节点目录 + 控制通道指令」的稳定接口层。
// 实现方为 internal/sfunode（节点管理专项）；消费方为语音编排、调度器、舞台、屏幕共享等模块。
// 通过 Set* 注入实现，默认实现为空目录 / 记日志的 no-op，保证未接入真实 SFU 时全链路可编译可测试。
package sfuctl

import (
	"log"
	"time"

	"github.com/google/uuid"
)

// NodeInfo 调度与配额计算所需的节点快照（docs 03 §2.2、docs 10 §3/§4）。
type NodeInfo struct {
	ID                   uuid.UUID         `json:"id"`
	DisplayName          string            `json:"display_name"`
	Region               string            `json:"region"`
	Labels               map[string]string `json:"labels"`
	Status               string            `json:"status"` // PENDING_ENROLLMENT/ENROLLED/ONLINE/DRAINING/DISABLED/REVOKED
	Online               bool              `json:"online"`
	Draining             bool              `json:"draining"`
	EnabledForScheduling bool              `json:"enabled_for_scheduling"`
	MaxUsers             int               `json:"max_users"`
	CurrentUsers         int               `json:"current_users"`
	CPUPercent           float64           `json:"cpu_pct"`
	MemPercent           float64           `json:"mem_pct"`
	BandwidthOutMbps     float64           `json:"bandwidth_out_mbps"`
	ScreenTracks         int               `json:"screen_tracks"`
	// WebRTCEndpoint 下发给客户端的接入提示（host:port 或 URL）。
	WebRTCEndpoint string `json:"webrtc_endpoint"`
	// CascadeEndpoint 节点级联 mTLS 信令端点 host:port（Register 自报，
	// 下发 CascadeEdge.parent_cascade_endpoint 时使用，docs 15 BG.2）。
	CascadeEndpoint string `json:"cascade_endpoint"`
	// NodeRTTMs 节点间 RTT 上报（key 为对端 node_id 字符串），级联/调度参考。
	NodeRTTMs map[string]float64 `json:"node_rtt_ms"`
	// LastSeenAt 最近一次心跳/注册时刻（内存注册表实时值优先，DB 快照兜底；
	// 零值表示无心跳记录）。供提前判死组合规则判断「心跳超过 N 个周期未到」
	//（docs 15 BI.2/BI.3），不下发客户端。
	LastSeenAt time.Time `json:"last_seen_at,omitempty"`
}

// Edge 级联边（docs 08 §4.2 / 15 BG.2）。
type Edge struct {
	ParentNodeID uuid.UUID `json:"parent_node_id"`
	ChildNodeID  uuid.UUID `json:"child_node_id"`
	// ParentCascadeEndpoint child 主动连 parent 的级联端口 host:port。
	ParentCascadeEndpoint string `json:"parent_cascade_endpoint"`
	// CascadeToken Server 签发的级联凭证（绑定 room+epoch+edge，短 TTL）。
	CascadeToken string `json:"cascade_token"`
}

// Directory 节点目录查询。
type Directory interface {
	// PoolNodes 返回某 Guild 节点池内的候选节点（含回落平台默认池语义，docs 07 专项 2.2）。
	PoolNodes(guildID uuid.UUID) ([]NodeInfo, error)
	// Node 查询单个节点。
	Node(nodeID uuid.UUID) (NodeInfo, error)
	// AllNodes 平台全部节点（系统管理端用）。
	AllNodes() ([]NodeInfo, error)
}

// Controller 控制通道指令（docs 03 §5.3、docs 08 §6.1）。实现须幂等。
type Controller interface {
	EnsureRoom(nodeID, roomID uuid.UUID) error
	CloseRoom(nodeID, roomID uuid.UUID) error
	DisconnectUser(nodeID, roomID, userID uuid.UUID, reason string) error
	UpdateParticipantCaps(nodeID, roomID, userID uuid.UUID, caps []string) error
	// SetAnchorLease 把租约下发给 nodeID 节点。SFU 侧每个涉边节点都要求
	// 「租约 epoch 与边集 epoch 匹配且未过期」才转发（docs 08 §3.1），
	// 因此编排方须向 anchor 与所有涉边节点分别下发。
	SetAnchorLease(nodeID, roomID, anchorNodeID uuid.UUID, epoch uint64, leaseExpire time.Time) error
	SetCascadeEdges(roomID uuid.UUID, epoch uint64, edges []Edge) error
	// WaitEdgeUp 阻塞等待某边在 epoch（或更高）就绪（SFU EdgeStatus 上报），
	// 超时返回错误（docs 08 §4.3 步骤 4：等边就绪再放行进房）。
	WaitEdgeUp(roomID uuid.UUID, epoch uint64, parentNodeID, childNodeID uuid.UUID, timeout time.Duration) error
	// DrainNode 通知节点进入排空（拒绝新会话）；deadline 为 09 I.6 硬迁时限
	//（零值表示不下发时限）。
	DrainNode(nodeID uuid.UUID, deadline time.Time) error
	// UndrainNode 取消排空（admin undrain）：节点恢复接受新会话。
	UndrainNode(nodeID uuid.UUID) error
	// MigrateParticipants 热迁移迁出指令（docs 09 §5.1 / 协议 MigrateParticipants）：
	// phase = MigratePhaseMark（CONNECT 阶段标记 sid 迁出中，继续服务）或
	// MigratePhaseExecute（CLEANUP 阶段 RemoveParticipant）。实现须幂等。
	MigrateParticipants(nodeID, roomID uuid.UUID, migrationID uuid.UUID, sessionIDs []string, toNodeID uuid.UUID, phase MigratePhase) error
}

// MigratePhase MigrateParticipants 的阶段语义（协议 MigrateParticipants.Phase）。
type MigratePhase string

const (
	MigratePhaseMark    MigratePhase = "MARK"
	MigratePhaseExecute MigratePhase = "EXECUTE"
)

var (
	dir Directory  = emptyDirectory{}
	ctl Controller = noopController{}
	// suspectReporter BI.3 提前信号上报（docs 15 §5）：把「某节点疑似故障」的
	// 独立信号（如客户端侧 ICE/连接失败上报）送入控制面判死聚合器。
	// 默认 no-op；由 sfubridge.Install 注入 sfucontrol.Registry.ReportSuspect。
	suspectReporter = func(nodeID uuid.UUID, origin string) {}
)

// SetDirectory / SetController 由 sfunode 模块 Register 时注入真实实现。
func SetDirectory(d Directory)   { dir = d }
func SetController(c Controller) { ctl = c }

// SetSuspectReporter 注入提前信号上报实现（判死权威仍在 Server 控制面，BI.4）。
func SetSuspectReporter(fn func(nodeID uuid.UUID, origin string)) {
	if fn != nil {
		suspectReporter = fn
	}
}

// ReportSuspect 上报一条针对 nodeID 的提前信号；origin 标识独立来源（去重键）。
func ReportSuspect(nodeID uuid.UUID, origin string) { suspectReporter(nodeID, origin) }

// Dir / Ctl 供消费方获取当前实现。
func Dir() Directory  { return dir }
func Ctl() Controller { return ctl }

type emptyDirectory struct{}

func (emptyDirectory) PoolNodes(uuid.UUID) ([]NodeInfo, error) { return nil, nil }
func (emptyDirectory) Node(uuid.UUID) (NodeInfo, error) {
	return NodeInfo{}, ErrNodeNotFound
}
func (emptyDirectory) AllNodes() ([]NodeInfo, error) { return nil, nil }

// ErrNodeNotFound 节点不存在。
var ErrNodeNotFound = errNodeNotFound{}

type errNodeNotFound struct{}

func (errNodeNotFound) Error() string { return "SFU 节点不存在" }

type noopController struct{}

func (noopController) EnsureRoom(nodeID, roomID uuid.UUID) error {
	log.Printf("sfuctl(noop): EnsureRoom node=%s room=%s", nodeID, roomID)
	return nil
}
func (noopController) CloseRoom(nodeID, roomID uuid.UUID) error {
	log.Printf("sfuctl(noop): CloseRoom node=%s room=%s", nodeID, roomID)
	return nil
}
func (noopController) DisconnectUser(nodeID, roomID, userID uuid.UUID, reason string) error {
	log.Printf("sfuctl(noop): DisconnectUser node=%s room=%s user=%s reason=%s", nodeID, roomID, userID, reason)
	return nil
}
func (noopController) UpdateParticipantCaps(nodeID, roomID, userID uuid.UUID, caps []string) error {
	log.Printf("sfuctl(noop): UpdateCaps node=%s room=%s user=%s caps=%v", nodeID, roomID, userID, caps)
	return nil
}
func (noopController) SetAnchorLease(nodeID, roomID, anchorNodeID uuid.UUID, epoch uint64, leaseExpire time.Time) error {
	log.Printf("sfuctl(noop): SetAnchorLease node=%s room=%s anchor=%s epoch=%d expire=%s",
		nodeID, roomID, anchorNodeID, epoch, leaseExpire.Format(time.RFC3339))
	return nil
}
func (noopController) SetCascadeEdges(roomID uuid.UUID, epoch uint64, edges []Edge) error {
	log.Printf("sfuctl(noop): SetCascadeEdges room=%s epoch=%d edges=%d", roomID, epoch, len(edges))
	return nil
}
func (noopController) WaitEdgeUp(roomID uuid.UUID, epoch uint64, parentNodeID, childNodeID uuid.UUID, timeout time.Duration) error {
	log.Printf("sfuctl(noop): WaitEdgeUp room=%s epoch=%d parent=%s child=%s", roomID, epoch, parentNodeID, childNodeID)
	return nil
}
func (noopController) DrainNode(nodeID uuid.UUID, deadline time.Time) error {
	log.Printf("sfuctl(noop): DrainNode node=%s deadline=%s", nodeID, deadline.Format(time.RFC3339))
	return nil
}
func (noopController) UndrainNode(nodeID uuid.UUID) error {
	log.Printf("sfuctl(noop): UndrainNode node=%s", nodeID)
	return nil
}
func (noopController) MigrateParticipants(nodeID, roomID uuid.UUID, migrationID uuid.UUID, sessionIDs []string, toNodeID uuid.UUID, phase MigratePhase) error {
	log.Printf("sfuctl(noop): MigrateParticipants node=%s room=%s migration=%s sids=%d to=%s phase=%s",
		nodeID, roomID, migrationID, len(sessionIDs), toNodeID, phase)
	return nil
}
