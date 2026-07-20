// edges.go 级联边状态跟踪（docs 08 §4.3 步骤 4 / §6.1 EdgeStatus）：
// SFU 经控制通道上报 EdgeUp/EdgeDown，Registry 维护最新状态并支持
// 「等待某边在某 epoch 就绪」的阻塞等待，供 join/迁移编排在下发边集后使用。
package sfucontrol

import (
	"context"
	"fmt"
	"time"

	owlsfuv1 "github.com/owlspeak/owl-server/backend/gen/owlsfu/v1"
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
func (r *Registry) UpdateEdgeStatus(es *owlsfuv1.EdgeStatus) {
	key := edgeKey{room: es.GetRoomId(), parent: es.GetParentNodeId(), child: es.GetChildNodeId()}
	up := es.GetState() == owlsfuv1.EdgeStatus_STATE_EDGE_UP
	now := time.Now()

	r.mu.Lock()
	if cur, ok := r.edges[key]; ok && es.GetEpoch() < cur.epoch {
		r.mu.Unlock()
		return
	}
	r.edges[key] = edgeState{epoch: es.GetEpoch(), up: up, rttMs: es.GetRttMs(), lastSeen: now}
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
