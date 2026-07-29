package auditundo

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/audit"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/perms"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UndoResponse 撤销 API 响应。
type UndoResponse struct {
	Original model.AuditLog `json:"original"`
	Undo     model.AuditLog `json:"undo"`
}

// Undo 执行审计条目的补偿撤销。
// guildScope 非空时要求日志属于该服（服级 API）；admin 全量 API 传 nil。
func Undo(deps appdeps.Deps, logID uuid.UUID, actor model.User, guildScope *uuid.UUID) (*UndoResponse, error) {
	var logRow model.AuditLog
	if err := deps.DB.First(&logRow, "id = ?", logID).Error; err != nil {
		return nil, errf(http.StatusNotFound, "NOT_FOUND", "审计记录不存在")
	}
	if guildScope != nil {
		if logRow.GuildID == nil || *logRow.GuildID != *guildScope {
			return nil, errf(http.StatusNotFound, "NOT_FOUND", "审计记录不存在")
		}
	}

	if logRow.UndoOfID != nil || logRow.Action == "audit.undo" {
		return nil, errf(http.StatusConflict, "CANNOT_UNDO_UNDO", "撤销记录不可再次撤销")
	}
	if logRow.UndoStatus == model.AuditUndoUndone || logRow.UndoneByID != nil {
		return nil, errf(http.StatusConflict, "ALREADY_UNDONE", "该操作已被撤销")
	}
	if !logRow.Reversible && logRow.UndoStatus != model.AuditUndoAvailable {
		hint := audit.HintOf(logRow.Action)
		msg := "该操作不可撤销"
		if hint != "" {
			msg = hint
		}
		return nil, errf(http.StatusConflict, "NOT_REVERSIBLE", msg)
	}

	spec, ok := Lookup(logRow.Action)
	if !ok {
		return nil, errf(http.StatusConflict, "NOT_REVERSIBLE", "该操作类型尚未支持一键撤销")
	}

	var guildCtx *perms.GuildContext
	if logRow.GuildID != nil {
		gctx, err := perms.LoadGuild(deps.DB, actor, *logRow.GuildID)
		if err != nil {
			if !actor.SystemAdmin {
				return nil, errf(http.StatusNotFound, "NOT_FOUND", "服务器不存在或无权访问")
			}
		} else {
			guildCtx = gctx
		}
	}

	uctx := Context{Deps: deps, Actor: actor, GuildCtx: guildCtx}
	if err := CheckPerm(uctx, spec); err != nil {
		return nil, err
	}

	// 查看审计权限（系统管除外）
	if !actor.SystemAdmin {
		if guildCtx == nil {
			return nil, errf(http.StatusForbidden, "MISSING_PERMISSION", "需要查看审计日志权限")
		}
		if !guildCtx.SystemAdmin && !guildCtx.Owner && !guildCtx.Has(rbac.ViewAuditLog) {
			return nil, errf(http.StatusForbidden, "MISSING_PERMISSION", "需要查看审计日志权限")
		}
	}

	result, err := spec.Handler(uctx, logRow)
	if err != nil {
		if e, ok := err.(*Error); ok {
			return nil, e
		}
		return nil, errf(http.StatusInternalServerError, "UNDO_FAILED", err.Error())
	}

	now := time.Now().UTC()
	detail := result.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	detail["original_action"] = logRow.Action
	detail["original_id"] = logRow.ID.String()
	if label := audit.LabelOf(logRow.Action); label != logRow.Action {
		detail["original_label"] = label
	}

	var undoRecord model.AuditLog
	err = deps.DB.Transaction(func(tx *gorm.DB) error {
		var locked model.AuditLog
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", logRow.ID).Error; err != nil {
			return err
		}
		if locked.UndoStatus == model.AuditUndoUndone || locked.UndoneByID != nil {
			return errf(http.StatusConflict, "ALREADY_UNDONE", "该操作已被撤销")
		}

		actorID := actor.ID
		actorType := "user"
		if actor.SystemAdmin {
			actorType = "system_admin"
		} else if guildCtx != nil && (guildCtx.Owner || guildCtx.Has(rbac.Administrator)) {
			actorType = "guild_admin"
		}
		targetType := result.TargetType
		if targetType == "" {
			targetType = logRow.TargetType
		}
		targetID := result.TargetID
		if targetID == "" {
			targetID = logRow.TargetID
		}

		created, err := audit.LogTx(tx, audit.Entry{
			ActorID:      &actorID,
			ActorType:    actorType,
			GuildID:      logRow.GuildID,
			Action:       "audit.undo",
			TargetType:   targetType,
			TargetID:     targetID,
			Detail:       detail,
			Before:       result.Before,
			After:        result.After,
			Irreversible: true,
			UndoOfID:     &logRow.ID,
		})
		if err != nil {
			return err
		}
		undoRecord = created

		updates := map[string]any{
			"undo_status":  model.AuditUndoUndone,
			"undone_by_id": created.ID,
			"undone_at":    now,
			"reversible":   false,
		}
		return tx.Model(&model.AuditLog{}).Where("id = ?", logRow.ID).Updates(updates).Error
	})
	if err != nil {
		if e, ok := err.(*Error); ok {
			return nil, e
		}
		return nil, errf(http.StatusInternalServerError, "UNDO_FAILED", "写入撤销审计失败: "+err.Error())
	}

	if deps.Bus != nil {
		for _, ev := range result.Events {
			deps.Bus.Publish(ev)
		}
	}

	_ = deps.DB.First(&logRow, "id = ?", logRow.ID)
	return &UndoResponse{Original: logRow, Undo: undoRecord}, nil
}

// EffectiveUndoStatus 列表 enrich 时的运行时状态（已 undone 优先）。
func EffectiveUndoStatus(log model.AuditLog) string {
	if log.UndoStatus == model.AuditUndoUndone || log.UndoneByID != nil {
		return model.AuditUndoUndone
	}
	if log.UndoOfID != nil || log.Action == "audit.undo" {
		return model.AuditUndoIrreversible
	}
	if log.UndoStatus == model.AuditUndoIrreversible {
		return model.AuditUndoIrreversible
	}
	if log.Reversible || log.UndoStatus == model.AuditUndoAvailable {
		if Has(log.Action) {
			return model.AuditUndoAvailable
		}
		// 标记可逆但尚未注册 handler：前端显示不可撤，避免假按钮
		return model.AuditUndoBlocked
	}
	if log.UndoStatus != "" && log.UndoStatus != model.AuditUndoNone {
		return log.UndoStatus
	}
	// 历史记录：若目录声明可逆且已有 handler，升级为 available 展示
	if info, ok := audit.Lookup(log.Action); ok && info.Reversible && Has(log.Action) {
		return model.AuditUndoAvailable
	}
	if info, ok := audit.Lookup(log.Action); ok && info.Irreversible {
		return model.AuditUndoIrreversible
	}
	return model.AuditUndoNone
}
