package sfucontrol

import (
	"context"
	"testing"
	"time"

	owlsfuv1 "github.com/newtspeak/newt-server/backend/gen/owlsfu/v1"
)

func edgeUp(room string, epoch uint64, parent, child string) *owlsfuv1.EdgeStatus {
	return &owlsfuv1.EdgeStatus{
		RoomId: room, Epoch: epoch, ParentNodeId: parent, ChildNodeId: child,
		State: owlsfuv1.EdgeStatus_STATE_EDGE_UP,
	}
}

func edgeDown(room string, epoch uint64, parent, child string) *owlsfuv1.EdgeStatus {
	return &owlsfuv1.EdgeStatus{
		RoomId: room, Epoch: epoch, ParentNodeId: parent, ChildNodeId: child,
		State: owlsfuv1.EdgeStatus_STATE_EDGE_DOWN,
	}
}

// TestWaitEdgeUp 覆盖：已就绪立即返回、上报后唤醒、epoch 校验（旧 epoch 不满足）、超时。
func TestWaitEdgeUp(t *testing.T) {
	r := NewRegistry()
	ctx := context.Background()

	// 1. 先上报后等待：立即返回。
	r.UpdateEdgeStatus(edgeUp("room", 1, "p", "c"))
	if err := r.WaitEdgeUp(ctx, "room", 1, "p", "c", 100*time.Millisecond); err != nil {
		t.Fatalf("已就绪的边应立即返回: %v", err)
	}

	// 2. 等待更高 epoch：旧 epoch 状态不满足，超时。
	if err := r.WaitEdgeUp(ctx, "room", 2, "p", "c", 100*time.Millisecond); err == nil {
		t.Fatal("epoch=1 的 EdgeUp 不应满足 epoch≥2 的等待")
	}

	// 3. 等待期间上报：唤醒。
	done := make(chan error, 1)
	go func() { done <- r.WaitEdgeUp(ctx, "room", 2, "p", "c", 3*time.Second) }()
	time.Sleep(50 * time.Millisecond)
	r.UpdateEdgeStatus(edgeUp("room", 2, "p", "c"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("上报 EdgeUp 后等待者应被唤醒: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待者未被唤醒")
	}

	// 4. EdgeDown 后不再满足。
	r.UpdateEdgeStatus(edgeDown("room", 2, "p", "c"))
	if r.EdgeUpNow("room", 2, "p", "c") {
		t.Fatal("EdgeDown 后 EdgeUpNow 应为 false")
	}

	// 5. 迟到的旧 epoch 上报不得覆盖新状态。
	r.UpdateEdgeStatus(edgeUp("room", 3, "p", "c"))
	r.UpdateEdgeStatus(edgeDown("room", 1, "p", "c")) // 迟到旧包
	if !r.EdgeUpNow("room", 3, "p", "c") {
		t.Fatal("旧 epoch 的迟到 EdgeDown 不应覆盖 epoch=3 的 EdgeUp")
	}
}
