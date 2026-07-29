// edges.go 级联边状态跟踪（docs 08 §4.3 步骤 4 / §6.1 EdgeStatus）：
// SFU 经控制通道上报 EdgeUp/EdgeDown，Registry 维护最新状态并支持
// 「等待某边在某 epoch 就绪」的阻塞等待，供 join/迁移编排在下发边集后使用。
// 同时缓存累计字节 / 路径类型，供管理台拓扑面板差分出实时 bps。
package sfucontrol

import (
	"context"
	"fmt"
	"time"

	owlsfuv1 "github.com/newtspeak/newt-server/backend/gen/owlsfu/v1"
)

// edgeKey 唯一标识一条级联边（房间内 parent+child 唯一）。
type edgeKey struct {
	room   string
	parent string
	child  string
}

// edgeState 某边的最新上报状态。
type edgeState struct {
	epoch    uint64
	up       bool
	rttMs    float64
	lastSeen time.Time
	// 拓扑可视化字段（EdgeStatus 扩展）
	bytesTx    uint64
	bytesRx    uint64
	pathType   string // lan / wan / unknown
	localIP    string
	remoteIP   string
	// 上报者视角：parent 侧上报的字节语义是 parent 本端 tx/rx。
	// 两端都可能上报；以 parent 视角为主聚合（见 ListEdges）。
	fromParent bool
}

// EdgeSnapshot 管理台拓扑用的边快照（跨房间可重复出现同一 parent-child 对，需按 room 区分）。
type EdgeSnapshot struct {
	RoomID       string    `json:"room_id"`
	Epoch        uint64    `json:"epoch"`
	ParentNodeID string    `json:"parent_node_id"`
	ChildNodeID  string    `json:"child_node_id"`
	Up           bool      `json:"up"`
	RttMs        float64   `json:"rtt_ms"`
	BytesTx      uint64    `json:"bytes_tx"` // parent → child 累计字节
	BytesRx      uint64    `json:"bytes_rx"` // child → parent 累计字节
	PathType     string    `json:"path_type"` // lan | wan | unknown
	LocalIP      string    `json:"local_ip,omitempty"`
	RemoteIP     string    `json:"remote_ip,omitempty"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

// edgeWaiter 一个等待「边就绪」的请求：epoch ≥ wantEpoch 且 up 即满足。
type edgeWaiter struct {
	key       edgeKey
	wantEpoch uint64
	ch        chan struct{}
}

// edgeStaleTTL 超过该时长未上报的边状态条目在更新时顺带清理（防长期泄漏）。
const edgeStaleTTL = 30 * time.Minute

// satisfied 判断状态是否满足等待条件。
func (w *edgeWaiter) satisfied(st edgeState, ok bool) bool {
	return ok && st.up && st.epoch >= w.wantEpoch
}

// UpdateEdgeStatus 记录一条 EdgeStatus 上报并唤醒满足条件的等待者。
// 旧 epoch 的上报不回退已知的更新状态（防迟到消息覆盖）。
// reporterID 可选：用于判断上报者是 parent 还是 child，统一字节语义到 parent 视角。
func (r *Registry) UpdateEdgeStatus(es *owlsfuv1.EdgeStatus) {
	r.UpdateEdgeStatusFrom(es, "")
}

// UpdateEdgeStatusFrom 同 UpdateEdgeStatus，并带上报者 node_id。
func (r *Registry) UpdateEdgeStatusFrom(es *owlsfuv1.EdgeStatus, reporterID string) {
	key := edgeKey{room: es.GetRoomId(), parent: es.GetParentNodeId(), child: es.GetChildNodeId()}
	up := es.GetState() == owlsfuv1.EdgeStatus_STATE_EDGE_UP
	now := time.Now()

	// 统一到 parent 视角：parent 的 tx = parent→child，rx = child→parent。
	// child 上报时本端 tx/rx 相反，需交换。
	bytesTx, bytesRx := es.GetBytesTx(), es.GetBytesRx()
	fromParent := true
	if reporterID != "" && reporterID == es.GetChildNodeId() {
		bytesTx, bytesRx = es.GetBytesRx(), es.GetBytesTx()
		fromParent = false
	} else if reporterID != "" && reporterID == es.GetParentNodeId() {
		fromParent = true
	}
	pathType := pathTypeString(es.GetPathType())
	localIP, remoteIP := es.GetLocalCandidateIp(), es.GetRemoteCandidateIp()
	// child 上报的 local/remote 对 parent 视角也需对调，便于展示「parent 侧 → child 侧」
	if !fromParent {
		localIP, remoteIP = remoteIP, localIP
	}

	r.mu.Lock()
	if cur, ok := r.edges[key]; ok && es.GetEpoch() < cur.epoch {
		r.mu.Unlock()
		return
	}
	// 同 epoch：优先保留 parent 上报的流量（两端都周期上报时避免 child 覆盖）。
	// 若当前无流量而新包有，或仅 child 上报过，则接受更新。
	if cur, ok := r.edges[key]; ok && es.GetEpoch() == cur.epoch {
		if cur.fromParent && !fromParent && (bytesTx > 0 || bytesRx > 0) && (cur.bytesTx > 0 || cur.bytesRx > 0) {
			// 已有 parent 流量快照：仍更新 RTT/up/path，但字节取 max 防回退
			if bytesTx < cur.bytesTx {
				bytesTx = cur.bytesTx
			}
			if bytesRx < cur.bytesRx {
				bytesRx = cur.bytesRx
			}
			// 路径：parent 已有明确类型时不降级
			if cur.pathType != "" && cur.pathType != "unknown" && pathType == "unknown" {
				pathType = cur.pathType
				localIP, remoteIP = cur.localIP, cur.remoteIP
			}
			fromParent = true
		}
		// 累计计数器不应回退
		if bytesTx < cur.bytesTx {
			bytesTx = cur.bytesTx
		}
		if bytesRx < cur.bytesRx {
			bytesRx = cur.bytesRx
		}
	}
	r.edges[key] = edgeState{
		epoch: es.GetEpoch(), up: up, rttMs: es.GetRttMs(), lastSeen: now,
		bytesTx: bytesTx, bytesRx: bytesRx, pathType: pathType,
		localIP: localIP, remoteIP: remoteIP, fromParent: fromParent,
	}
	// 顺带清理长期无上报的条目
	for k, st := range r.edges {
		if now.Sub(st.lastSeen) > edgeStaleTTL {
			delete(r.edges, k)
		}
	}
	var wake []*edgeWaiter
	if up {
		remaining := r.edgeWaiters[:0]
		for _, w := range r.edgeWaiters {
			if w.key == key && w.satisfied(r.edges[key], true) {
				wake = append(wake, w)
			} else {
				remaining = append(remaining, w)
			}
		}
		r.edgeWaiters = remaining
	}
	r.mu.Unlock()

	for _, w := range wake {
		close(w.ch)
	}
}

func pathTypeString(t owlsfuv1.EdgeStatus_PathType) string {
	switch t {
	case owlsfuv1.EdgeStatus_PATH_TYPE_LAN:
		return "lan"
	case owlsfuv1.EdgeStatus_PATH_TYPE_WAN:
		return "wan"
	default:
		return "unknown"
	}
}

// ListEdges 返回当前内存中全部级联边快照（含 DOWN 且未过期条目）。
func (r *Registry) ListEdges() []EdgeSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]EdgeSnapshot, 0, len(r.edges))
	for k, st := range r.edges {
		out = append(out, EdgeSnapshot{
			RoomID:       k.room,
			Epoch:        st.epoch,
			ParentNodeID: k.parent,
			ChildNodeID:  k.child,
			Up:           st.up,
			RttMs:        st.rttMs,
			BytesTx:      st.bytesTx,
			BytesRx:      st.bytesRx,
			PathType:     st.pathType,
			LocalIP:      st.localIP,
			RemoteIP:     st.remoteIP,
			LastSeenAt:   st.lastSeen,
		})
	}
	return out
}

// EdgeUpNow 查询某边当前是否已就绪（epoch ≥ wantEpoch 且 up）。
func (r *Registry) EdgeUpNow(roomID string, wantEpoch uint64, parent, child string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.edges[edgeKey{room: roomID, parent: parent, child: child}]
	return ok && st.up && st.epoch >= wantEpoch
}

// WaitEdgeUp 阻塞等待某边在 wantEpoch（或更高）就绪；超时返回错误
// （docs 08 §4.3：等待边就绪或超时失败/换节点）。
func (r *Registry) WaitEdgeUp(ctx context.Context, roomID string, wantEpoch uint64, parent, child string, timeout time.Duration) error {
	key := edgeKey{room: roomID, parent: parent, child: child}
	w := &edgeWaiter{key: key, wantEpoch: wantEpoch, ch: make(chan struct{})}

	r.mu.Lock()
	if st, ok := r.edges[key]; w.satisfied(st, ok) {
		r.mu.Unlock()
		return nil
	}
	r.edgeWaiters = append(r.edgeWaiters, w)
	r.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.ch:
		return nil
	case <-ctx.Done():
		r.removeEdgeWaiter(w)
		return ctx.Err()
	case <-timer.C:
		r.removeEdgeWaiter(w)
		return fmt.Errorf("等待级联边就绪超时（room=%s parent=%s child=%s epoch=%d）",
			roomID, parent, child, wantEpoch)
	}
}

func (r *Registry) removeEdgeWaiter(w *edgeWaiter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, x := range r.edgeWaiters {
		if x == w {
			r.edgeWaiters = append(r.edgeWaiters[:i], r.edgeWaiters[i+1:]...)
			return
		}
	}
}
