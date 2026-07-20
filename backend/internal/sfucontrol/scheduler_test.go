package sfucontrol

import (
	"testing"

	"github.com/google/uuid"
)

func TestPickNodeLowestLoad(t *testing.T) {
	busy := uuid.New()
	idle := uuid.New()
	offline := uuid.New()
	disabled := uuid.New()
	full := uuid.New()
	picked, ok := PickNode([]Candidate{
		{NodeID: busy, Online: true, EnabledForScheduling: true, CurrentUsers: 120, MaxUsers: 1000},
		{NodeID: idle, Online: true, EnabledForScheduling: true, CurrentUsers: 3, MaxUsers: 1000},
		{NodeID: offline, Online: false, EnabledForScheduling: true, CurrentUsers: 0, MaxUsers: 1000},
		{NodeID: disabled, Online: true, EnabledForScheduling: false, CurrentUsers: 0, MaxUsers: 1000},
		{NodeID: full, Online: true, EnabledForScheduling: true, CurrentUsers: 0, MaxUsers: 0},
	})
	if !ok {
		t.Fatal("应能选出节点")
	}
	if picked != idle {
		t.Fatalf("应选最低占用的在线可调度节点 %s，实际 %s", idle, picked)
	}
}

func TestPickNodeNoCandidates(t *testing.T) {
	if _, ok := PickNode(nil); ok {
		t.Fatal("空候选不应选出节点")
	}
	if _, ok := PickNode([]Candidate{
		{NodeID: uuid.New(), Online: false, EnabledForScheduling: true, MaxUsers: 100},
		{NodeID: uuid.New(), Online: true, EnabledForScheduling: false, MaxUsers: 100},
	}); ok {
		t.Fatal("无在线可调度节点时不应选出")
	}
}

func TestPickNodeSkipsFullNodes(t *testing.T) {
	full := uuid.New()
	spare := uuid.New()
	picked, ok := PickNode([]Candidate{
		{NodeID: full, Online: true, EnabledForScheduling: true, CurrentUsers: 100, MaxUsers: 100},
		{NodeID: spare, Online: true, EnabledForScheduling: true, CurrentUsers: 99, MaxUsers: 100},
	})
	if !ok || picked != spare {
		t.Fatalf("满载节点应被跳过，应选 %s，实际 %s (ok=%v)", spare, picked, ok)
	}
	// 全部满载 → 无可用节点（HTTP 层返回 503 NO_SFU_CAPACITY）。
	if _, ok := PickNode([]Candidate{
		{NodeID: full, Online: true, EnabledForScheduling: true, CurrentUsers: 100, MaxUsers: 100},
	}); ok {
		t.Fatal("全部满载时不应选出节点")
	}
}
