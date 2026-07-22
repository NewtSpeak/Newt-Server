package sfucontrol

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	owlsfuv1 "github.com/owlspeak/owl-server/backend/gen/owlsfu/v1"
)

// ErrNodeOffline 节点当前没有活跃控制流。
var ErrNodeOffline = fmt.Errorf("SFU 节点不在线")

// ErrCommandTimeout 等待 CommandAck 超时。
var ErrCommandTimeout = fmt.Errorf("等待 SFU CommandAck 超时")

const defaultCommandTimeout = 5 * time.Second

// Capacity 心跳上报的容量快照。
type Capacity struct {
	MaxUsers         int     `json:"max_users"`
	CurrentUsers     int     `json:"current_users"`
	RoomCount        int     `json:"room_count"`
	CPUPct           float64 `json:"cpu_pct"`
	MemPct           float64 `json:"mem_pct"`
	BandwidthOutMbps float64 `json:"bandwidth_out_mbps"`
	ScreenTracks     int     `json:"screen_tracks"`
}

// Snapshot 注册表内单节点的运行时状态。
type Snapshot struct {
	NodeID   uuid.UUID `json:"node_id"`
	Online   bool      `json:"online"`
	Capacity Capacity  `json:"capacity"`
	LastSeen time.Time `json:"last_seen"`
}

// sender 控制流发送端（生产实现为 gRPC 双向流；测试可注入假实现）。
type sender interface {
	Send(*owlsfuv1.ServerMessage) error
}

// conn 一条活跃控制流。写入经 sendMu 串行化（RegisterAck 与指令来自不同 goroutine）。
type conn struct {
	nodeID    uuid.UUID
	stream    sender
	sendMu    sync.Mutex
	done      chan struct{}
	closeOnce sync.Once
}

func (c *conn) send(message *owlsfuv1.ServerMessage) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	select {
	case <-c.done:
		return ErrNodeOffline
	default:
	}
	return c.stream.Send(message)
}

func (c *conn) close() { c.closeOnce.Do(func() { close(c.done) }) }

type nodeState struct {
	// online 心跳健康（判死语义：连续 3 个心跳周期未上报置 false，15 BI.1；
	// 或 BI.3 提前判死组合规则满足，见 earlydeath.go）。
	online bool
	// attached 当前是否有活跃控制流（实时调度视图；流断开立即置 false，
	// 但状态保留，由心跳监控在硬判死窗口后统一宣告节点死亡）。
	attached bool
	capacity Capacity
	lastSeen time.Time
	// suspicions BI.3 提前信号：origin → 最近上报时刻（EdgeDown 指控 /
	// 客户端 ICE 失败上报等）；Attach 重连时随状态整体重建自动清空。
	suspicions map[string]time.Time
}

// Registry 内存节点注册表：node_id → 控制流 + 容量 + 在线状态；线程安全。
// 同时维护级联边状态（EdgeStatus 上报 + WaitEdgeUp 等待，见 edges.go）。
type Registry struct {
	mu             sync.Mutex
	conns          map[uuid.UUID]*conn
	states         map[uuid.UUID]*nodeState
	pending        map[string]chan *owlsfuv1.CommandAck
	edges          map[edgeKey]edgeState
	edgeWaiters    []*edgeWaiter
	commandTimeout time.Duration
}

func NewRegistry() *Registry {
	return &Registry{
		conns:          map[uuid.UUID]*conn{},
		states:         map[uuid.UUID]*nodeState{},
		pending:        map[string]chan *owlsfuv1.CommandAck{},
		edges:          map[edgeKey]edgeState{},
		commandTimeout: defaultCommandTimeout,
	}
}

// Attach 登记控制流；同一节点已有旧流时先关闭旧流（新流优先）。
func (r *Registry) Attach(nodeID uuid.UUID, stream sender) *conn {
	newConn := &conn{nodeID: nodeID, stream: stream, done: make(chan struct{})}
	r.mu.Lock()
	old := r.conns[nodeID]
	r.conns[nodeID] = newConn
	r.states[nodeID] = &nodeState{online: true, attached: true, lastSeen: time.Now()}
	r.mu.Unlock()
	if old != nil {
		old.close()
	}
	return newConn
}

// Detach 流结束时移除连接；仅当仍指向同一条流时生效（避免新流被旧流的清理误删）。
// 状态条目保留（attached=false，lastSeen 冻结），使心跳监控能在
// 「3 个心跳周期未上报」窗口后对断流节点做统一判死（15 BI.1；瞬断重连由 Attach 覆盖恢复）。
func (r *Registry) Detach(nodeID uuid.UUID, c *conn) {
	c.close()
	r.mu.Lock()
	if r.conns[nodeID] == c {
		delete(r.conns, nodeID)
		if state, ok := r.states[nodeID]; ok {
			state.attached = false
		}
	}
	r.mu.Unlock()
}

// Disconnect 强制断开某节点控制流（revoke 场景）。
func (r *Registry) Disconnect(nodeID uuid.UUID) {
	r.mu.Lock()
	c := r.conns[nodeID]
	r.mu.Unlock()
	if c != nil {
		c.close()
	}
}

// UpdateCapacity 心跳/注册时刷新容量与在线状态。
func (r *Registry) UpdateCapacity(nodeID uuid.UUID, capacity Capacity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[nodeID]
	if !ok {
		return
	}
	state.capacity = capacity
	state.lastSeen = time.Now()
	state.online = true
}

// Snapshot 查询单节点运行时状态。Online = 控制流活跃且心跳健康（实时调度视图）。
func (r *Registry) Snapshot(nodeID uuid.UUID) (Snapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[nodeID]
	if !ok {
		return Snapshot{}, false
	}
	return Snapshot{NodeID: nodeID, Online: state.attached && state.online, Capacity: state.capacity, LastSeen: state.lastSeen}, true
}

// MarkStale 将超过 threshold 未心跳的节点标记为离线（判死），返回本次新标记的节点。
// 覆盖两种情形：控制流仍在但心跳停发；控制流已断且未在窗口内重连（Detach 保留状态）。
func (r *Registry) MarkStale(threshold time.Duration) []uuid.UUID {
	now := time.Now()
	var stale []uuid.UUID
	r.mu.Lock()
	defer r.mu.Unlock()
	for nodeID, state := range r.states {
		if state.online && now.Sub(state.lastSeen) > threshold {
			state.online = false
			stale = append(stale, nodeID)
		}
	}
	return stale
}

// SendCommand 生成 command_id、写入控制流并等待 CommandAck（默认 5s 超时）。
// payload 只需填 oneof 载荷，command_id 由本方法生成。
func (r *Registry) SendCommand(ctx context.Context, nodeID uuid.UUID, command *owlsfuv1.Command) (*owlsfuv1.CommandAck, error) {
	r.mu.Lock()
	c := r.conns[nodeID]
	r.mu.Unlock()
	if c == nil {
		return nil, ErrNodeOffline
	}

	command.CommandId = uuid.NewString()
	ackCh := make(chan *owlsfuv1.CommandAck, 1)
	r.mu.Lock()
	r.pending[command.CommandId] = ackCh
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, command.CommandId)
		r.mu.Unlock()
	}()

	if err := c.send(&owlsfuv1.ServerMessage{Payload: &owlsfuv1.ServerMessage_Command{Command: command}}); err != nil {
		return nil, err
	}

	// 超时优先取 ctx 剩余截止时间（升级下载等长任务会传 10+ 分钟 ctx）；
	// 无截止时间时回落 registry 默认 commandTimeout。
	timeout := r.commandTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remain := time.Until(deadline); remain > 0 {
			timeout = remain
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case ack := <-ackCh:
		return ack, nil
	case <-c.done:
		return nil, ErrNodeOffline
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, ErrCommandTimeout
	}
}

// Resolve 控制流收到 CommandAck 时完成对应 pending command。
func (r *Registry) Resolve(ack *owlsfuv1.CommandAck) {
	r.mu.Lock()
	ch := r.pending[ack.GetCommandId()]
	delete(r.pending, ack.GetCommandId())
	r.mu.Unlock()
	if ch != nil {
		ch <- ack
	}
}
