package sfunode

import (
	"testing"

	"github.com/owlspeak/owl-server/backend/internal/model"
)

func TestStateMachine(t *testing.T) {
	allowed := []struct{ from, to string }{
		{model.SfuNodePendingEnrollment, model.SfuNodeEnrolled},
		{model.SfuNodePendingEnrollment, model.SfuNodeDisabled},
		{model.SfuNodePendingEnrollment, model.SfuNodeRevoked},
		{model.SfuNodeEnrolled, model.SfuNodeOnline},
		{model.SfuNodeEnrolled, model.SfuNodeDisabled},
		{model.SfuNodeEnrolled, model.SfuNodeRevoked},
		{model.SfuNodeOnline, model.SfuNodeDraining},
		{model.SfuNodeOnline, model.SfuNodeEnrolled},
		{model.SfuNodeOnline, model.SfuNodeDisabled},
		{model.SfuNodeOnline, model.SfuNodeRevoked},
		{model.SfuNodeDraining, model.SfuNodeOnline},
		{model.SfuNodeDraining, model.SfuNodeEnrolled},
		{model.SfuNodeDraining, model.SfuNodeDisabled},
		{model.SfuNodeDraining, model.SfuNodeRevoked},
		{model.SfuNodeDisabled, model.SfuNodeEnrolled},
		{model.SfuNodeDisabled, model.SfuNodeRevoked},
	}
	for _, tc := range allowed {
		if !CanTransition(tc.from, tc.to) {
			t.Errorf("期望允许 %s → %s，实际被拒绝", tc.from, tc.to)
		}
	}

	forbidden := []struct{ from, to string }{
		// 未 enroll 不能直接上线/排空
		{model.SfuNodePendingEnrollment, model.SfuNodeOnline},
		{model.SfuNodePendingEnrollment, model.SfuNodeDraining},
		// ENROLLED 不能直接 DRAINING（必须先在线）
		{model.SfuNodeEnrolled, model.SfuNodeDraining},
		// 不能回到 PENDING
		{model.SfuNodeEnrolled, model.SfuNodePendingEnrollment},
		{model.SfuNodeOnline, model.SfuNodePendingEnrollment},
		// REVOKED 是终态
		{model.SfuNodeRevoked, model.SfuNodeEnrolled},
		{model.SfuNodeRevoked, model.SfuNodeOnline},
		{model.SfuNodeRevoked, model.SfuNodeDisabled},
		// DISABLED 不能直接上线（先 ENROLLED 再连接）
		{model.SfuNodeDisabled, model.SfuNodeOnline},
		// 自环
		{model.SfuNodeOnline, model.SfuNodeOnline},
	}
	for _, tc := range forbidden {
		if CanTransition(tc.from, tc.to) {
			t.Errorf("期望拒绝 %s → %s，实际被允许", tc.from, tc.to)
		}
	}

	if _, err := Transition(model.SfuNodeRevoked, model.SfuNodeOnline); err == nil {
		t.Fatal("Transition 对非法转换应返回错误")
	}
	if to, err := Transition(model.SfuNodeEnrolled, model.SfuNodeOnline); err != nil || to != model.SfuNodeOnline {
		t.Fatalf("Transition 合法转换失败: %v", err)
	}
}
