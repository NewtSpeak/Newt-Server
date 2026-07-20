package sfucontrol

import "github.com/google/uuid"

// Candidate 调度器 v0 的候选节点（DB 状态 + 注册表运行时状态的合并视图）。
type Candidate struct {
	NodeID               uuid.UUID
	Online               bool
	EnabledForScheduling bool
	CurrentUsers         int
	MaxUsers             int
}

// PickNode 调度器 v0（docs 10 仅取 v0 子集）：
// 在线、开启调度且未满载（current_users < max_users）的节点里选当前用户数最小者；
// max_users 未上报（<=0）视为无容量，不参与调度。无可用节点返回 false。
func PickNode(candidates []Candidate) (uuid.UUID, bool) {
	var best *Candidate
	for i := range candidates {
		candidate := &candidates[i]
		if !candidate.Online || !candidate.EnabledForScheduling {
			continue
		}
		if candidate.MaxUsers <= 0 || candidate.CurrentUsers >= candidate.MaxUsers {
			continue
		}
		if best == nil || candidate.CurrentUsers < best.CurrentUsers {
			best = candidate
		}
	}
	if best == nil {
		return uuid.Nil, false
	}
	return best.NodeID, true
}
