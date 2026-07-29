package voice

// refresh-token 迁移窗口 bug 修复单测（M4 遗留）：
// 迁移 PREPARE 后 VoiceState.sid 已是新会话而 node_id 仍指旧节点，
// 此窗口内续签必须绑定 job.to_node（新会话所在节点）或让客户端等
// VOICE_SERVER_UPDATE，绝不能签出「旧节点 + 新 sid」的无效组合。

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

func TestRefreshTokenBinding(t *testing.T) {
	oldNode, newNode := uuid.New(), uuid.New()
	vs := model.VoiceState{NodeID: &oldNode}

	// 无在途迁移：按当前节点签发（原行为）。
	if node, err := refreshTokenBinding(vs, nil); err != nil || node != oldNode {
		t.Fatalf("无迁移应按当前节点签发: node=%v err=%v", node, err)
	}

	// CONNECT/CUTOVER/CLEANUP（to_node 已定）：按目标节点签发。
	for _, state := range []string{
		model.MigrationStateConnect, model.MigrationStateCutover, model.MigrationStateCleanup,
	} {
		job := &model.VoiceMigrationJob{State: state, ToNodeID: &newNode}
		node, err := refreshTokenBinding(vs, job)
		if err != nil || node != newNode {
			t.Fatalf("state=%s 应按 to_node 签发: node=%v err=%v", state, node, err)
		}
		if node == oldNode {
			t.Fatalf("state=%s 绝不能按旧节点签发（旧节点无新 sid 会话）", state)
		}
	}

	// QUEUED/PREPARE/FAILED（目标未定或新 token 未生成）：等 VOICE_SERVER_UPDATE。
	for _, state := range []string{
		model.MigrationStateQueued, model.MigrationStatePrepare, model.MigrationStateFailed,
	} {
		if _, err := refreshTokenBinding(vs, &model.VoiceMigrationJob{State: state}); !errors.Is(err, errRefreshDuringMigration) {
			t.Fatalf("state=%s 应返回 errRefreshDuringMigration，got %v", state, err)
		}
	}

	// CONNECT 但 to_node 缺失（异常防御）：同样让客户端等待。
	if _, err := refreshTokenBinding(vs, &model.VoiceMigrationJob{State: model.MigrationStateConnect}); !errors.Is(err, errRefreshDuringMigration) {
		t.Fatalf("CONNECT 无 to_node 应返回 errRefreshDuringMigration，got %v", err)
	}
}
