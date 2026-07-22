package voice

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/sfuctl"
	"gorm.io/gorm"
)

// 滚动升级默认时限：先 Drain 排空（docs 09 I.6 60s 硬迁 + 余量），再等会话清零。
const (
	defaultRollingDrainTimeout = 90 * time.Second
	rollingPollInterval        = 500 * time.Millisecond
)

// RollingUpgradeOptions 升级前排空参数。
type RollingUpgradeOptions struct {
	// Timeout 等待节点迁空的最长时间；≤0 用 defaultRollingDrainTimeout。
	Timeout time.Duration
	// ActorID 可选审计主体（系统管理员）。
	ActorID *uuid.UUID
}

// RollingUpgradeResult 排空结果摘要（供管理 API 返回）。
type RollingUpgradeResult struct {
	SessionsBefore int  `json:"sessions_before"`
	SessionsAfter  int  `json:"sessions_after"`
	JobsCreated    int  `json:"jobs_created"`
	Drained        bool `json:"drained"`             // 超时前是否已迁空
	ElapsedMs      int64 `json:"elapsed_ms"`          // 实际等待时长（毫秒）
	Forced         bool `json:"forced,omitempty"`    // 超时后仍有残留
}

// DrainForRollingUpgrade 滚动升级前置步骤（用户基本无感）：
//  1. 关闭调度开关（禁止新会话落到该节点）；
//  2. 节点状态 → DRAINING，下发 Drain 指令；
//  3. 触发批量迁移：用户均匀分到「附近」可用节点（同 region 优先）；
//  4. 轮询等待该节点会话清零（或超时）；
//  5. 返回摘要；调用方再下发 UpdateBinary。
//
// 升级完成后节点重新 Register 为 ONLINE 时，由调用方或 Register 路径恢复
// enabled_for_scheduling（见 restoreSchedulingAfterUpgrade）。
func DrainForRollingUpgrade(ctx context.Context, nodeID uuid.UUID, opts RollingUpgradeOptions) (RollingUpgradeResult, error) {
	svc := sharedService
	if svc == nil {
		return RollingUpgradeResult{}, fmt.Errorf("voice 模块未初始化")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultRollingDrainTimeout
	}

	var before int64
	if err := svc.db.Model(&model.VoiceState{}).
		Where("node_id = ? AND channel_id IS NOT NULL", nodeID).Count(&before).Error; err != nil {
		return RollingUpgradeResult{}, fmt.Errorf("统计会话失败: %w", err)
	}
	result := RollingUpgradeResult{SessionsBefore: int(before)}

	// 1) 关调度：新 Join 不再选中该节点。
	if err := svc.db.Model(&model.SfuNode{}).Where("id = ?", nodeID).
		Update("enabled_for_scheduling", false).Error; err != nil {
		return result, fmt.Errorf("关闭调度开关失败: %w", err)
	}

	// 2) 状态机 → DRAINING（非法转换则仅关调度 + 迁移）。
	var node model.SfuNode
	if err := svc.db.First(&node, "id = ?", nodeID).Error; err != nil {
		return result, fmt.Errorf("节点不存在")
	}
	if node.Status != model.SfuNodeDraining && node.Status != model.SfuNodeDisabled && node.Status != model.SfuNodeRevoked {
		if err := svc.db.Model(&node).Update("status", model.SfuNodeDraining).Error; err != nil {
			return result, fmt.Errorf("标记 DRAINING 失败: %w", err)
		}
		node.Status = model.SfuNodeDraining
	}
	// 下发 SFU 侧 Drain（拒绝新会话）；失败不阻断（节点可能已半离线）。
	_ = sfuctl.Ctl().DrainNode(nodeID, time.Now().Add(timeout))

	// 事件：与手动 drain 对齐，便于其它订阅者感知。
	if svc.bus != nil {
		svc.bus.Publish(eventbus.Event{
			Type:    eventbus.InternalNodeDraining,
			Payload: map[string]any{"node_id": nodeID.String(), "status": model.SfuNodeDraining},
		})
	}

	// 3) 若尚无会话，直接成功。
	if before == 0 {
		result.Drained = true
		log.Printf("voice: 滚动升级节点 %s 无会话，跳过迁移", nodeID)
		return result, nil
	}

	// 4) 显式触发均匀分流迁移（事件路径也会再触发一次，createJob 幂等合并）。
	// 直接调用 engine，避免依赖总线异步时序。
	startJobs := countActiveMigrationsFrom(svc.db, nodeID)
	svc.engine.migrateNode(nodeID, model.MigrationReasonDrain)
	result.JobsCreated = countActiveMigrationsFrom(svc.db, nodeID) - startJobs
	if result.JobsCreated < 0 {
		result.JobsCreated = countActiveMigrationsFrom(svc.db, nodeID)
	}

	// 5) 等待迁空。
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(rollingPollInterval)
	defer ticker.Stop()
	started := time.Now()
	for {
		var remaining int64
		_ = svc.db.Model(&model.VoiceState{}).
			Where("node_id = ? AND channel_id IS NOT NULL", nodeID).Count(&remaining)
		if remaining == 0 {
			result.SessionsAfter = 0
			result.Drained = true
			result.ElapsedMs = time.Since(started).Milliseconds()
			log.Printf("voice: 滚动升级节点 %s 已迁空（%d 会话，耗时 %dms）",
				nodeID, before, result.ElapsedMs)
			return result, nil
		}
		if time.Now().After(deadline) {
			result.SessionsAfter = int(remaining)
			result.Drained = false
			result.Forced = true
			result.ElapsedMs = time.Since(started).Milliseconds()
			log.Printf("voice: 滚动升级节点 %s 等待迁空超时，残留 %d 会话（仍继续升级）",
				nodeID, remaining)
			return result, nil
		}
		select {
		case <-ctx.Done():
			result.SessionsAfter = int(remaining)
			result.ElapsedMs = time.Since(started).Milliseconds()
			return result, ctx.Err()
		case <-ticker.C:
		}
	}
}

// RestoreSchedulingAfterUpgrade 节点升级重启后恢复调度（若仍 DRAINING 且已上线则回 ONLINE）。
// 由 update-binary 成功路径异步调用；节点 Register 时也可再次调用保证幂等。
func RestoreSchedulingAfterUpgrade(nodeID uuid.UUID) {
	svc := sharedService
	if svc == nil {
		return
	}
	var node model.SfuNode
	if err := svc.db.First(&node, "id = ?", nodeID).Error; err != nil {
		return
	}
	updates := map[string]any{"enabled_for_scheduling": true}
	// DRAINING 且控制通道已在 → 回到 ONLINE；否则保持现状（可能仍在重启）。
	if node.Status == model.SfuNodeDraining || node.Status == model.SfuNodeEnrolled {
		if info, err := sfuctl.Dir().Node(nodeID); err == nil && info.Online {
			updates["status"] = model.SfuNodeOnline
		} else if node.Status == model.SfuNodeDraining {
			// 尚未连上：先落到 ENROLLED，等 Register 刷回 ONLINE。
			updates["status"] = model.SfuNodeEnrolled
		}
	}
	_ = svc.db.Model(&model.SfuNode{}).Where("id = ?", nodeID).Updates(updates)
	// 通知节点取消排空（可接新会话）。
	_ = sfuctl.Ctl().UndrainNode(nodeID)
	log.Printf("voice: 滚动升级后恢复节点 %s 调度 enabled=true", nodeID)
}

// CountNodeSessions 返回节点上活跃语音会话数（管理 API 预检用）。
func CountNodeSessions(nodeID uuid.UUID) (int, error) {
	if sharedService == nil {
		return 0, fmt.Errorf("voice 模块未初始化")
	}
	var n int64
	err := sharedService.db.Model(&model.VoiceState{}).
		Where("node_id = ? AND channel_id IS NOT NULL", nodeID).Count(&n).Error
	return int(n), err
}

func countActiveMigrationsFrom(db *gorm.DB, fromNode uuid.UUID) int {
	var n int64
	_ = db.Model(&model.VoiceMigrationJob{}).
		Where("from_node_id = ? AND state NOT IN ?", fromNode,
			[]string{model.MigrationStateDone, model.MigrationStateCanceled}).
		Count(&n)
	return int(n)
}
