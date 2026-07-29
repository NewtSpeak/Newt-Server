package sfunode

import (
	"fmt"

	"github.com/newtspeak/newt-server/backend/internal/model"
)

// validTransitions 节点状态机（docs 03 §8）：
//
//	PENDING_ENROLLMENT ──enroll──► ENROLLED ──连上──► ONLINE ⇄ DRAINING
//	任意非终态可 DISABLED / REVOKED；REVOKED 为终态。
var validTransitions = map[string]map[string]bool{
	model.SfuNodePendingEnrollment: {
		model.SfuNodeEnrolled: true, // enroll 成功
		model.SfuNodeDisabled: true, // 超时/取消占位
		model.SfuNodeRevoked:  true,
	},
	model.SfuNodeEnrolled: {
		model.SfuNodeOnline:   true, // 控制通道连上
		model.SfuNodeDisabled: true,
		model.SfuNodeRevoked:  true,
	},
	model.SfuNodeOnline: {
		model.SfuNodeEnrolled: true, // 心跳失败/断连 → 离线
		model.SfuNodeDraining: true,
		model.SfuNodeDisabled: true,
		model.SfuNodeRevoked:  true,
	},
	model.SfuNodeDraining: {
		model.SfuNodeOnline:   true, // 排空结束且仍在线
		model.SfuNodeEnrolled: true, // 排空结束且已离线
		model.SfuNodeDisabled: true,
		model.SfuNodeRevoked:  true,
	},
	model.SfuNodeDisabled: {
		model.SfuNodeEnrolled: true, // 重新启用（需重新连上才 ONLINE）
		model.SfuNodeRevoked:  true,
	},
	model.SfuNodeRevoked: {}, // 终态
}

// CanTransition 判断状态迁移是否合法。
func CanTransition(from, to string) bool { return validTransitions[from][to] }

// Transition 校验并返回目标状态；非法迁移返回中文错误。
func Transition(from, to string) (string, error) {
	if !CanTransition(from, to) {
		return "", fmt.Errorf("非法状态转换：%s → %s", from, to)
	}
	return to, nil
}
