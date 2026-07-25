// Package audit 提供审计写入助手与可逆性目录；敏感操作必须调用 Log。
package audit

import (
	"encoding/json"
	"log"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
)

// Entry 一条审计记录的输入。
type Entry struct {
	ActorID    *uuid.UUID
	ActorType  string // user / system_admin / guild_admin / auto / node
	GuildID    *uuid.UUID
	Action     string
	TargetType string
	TargetID   string
	Detail     map[string]any
	// Before / After：机器可恢复快照；若为空会尝试从 Detail["before"] / Detail["after"] 提取。
	Before map[string]any
	After  map[string]any
	// Reversible / Irreversible：显式覆盖目录默认；二者皆 false 时走 Catalog。
	Reversible   bool
	Irreversible bool
	// UndoOfID：本条为撤销动作时指向原审计 ID。
	UndoOfID *uuid.UUID
}

// Log 尽力写入审计记录；失败仅记日志，不阻断业务主路径。
func Log(db *gorm.DB, entry Entry) {
	if _, err := LogRecord(db, entry); err != nil {
		log.Printf("audit: 写入审计失败 action=%s err=%v", entry.Action, err)
	}
}

// LogTx 在既有事务中写入；失败返回 error（供撤销等同事务路径使用）。
func LogTx(tx *gorm.DB, entry Entry) (model.AuditLog, error) {
	return LogRecord(tx, entry)
}

// LogRecord 写入并返回完整记录。
func LogRecord(db *gorm.DB, entry Entry) (model.AuditLog, error) {
	detailMap := entry.Detail
	if detailMap == nil {
		detailMap = map[string]any{}
	}
	before := entry.Before
	after := entry.After
	if before == nil {
		if raw, ok := detailMap["before"]; ok {
			if m, ok := asMap(raw); ok {
				before = m
			}
		}
	}
	if after == nil {
		if raw, ok := detailMap["after"]; ok {
			if m, ok := asMap(raw); ok {
				after = m
			}
		}
	}

	reversible, undoStatus := resolveReversibility(entry, before)

	detail := "{}"
	if raw, err := json.Marshal(detailMap); err == nil {
		detail = string(raw)
	}
	beforeJSON := "{}"
	if before != nil {
		if raw, err := json.Marshal(before); err == nil {
			beforeJSON = string(raw)
		}
	}
	afterJSON := "{}"
	if after != nil {
		if raw, err := json.Marshal(after); err == nil {
			afterJSON = string(raw)
		}
	}
	if entry.ActorType == "" {
		entry.ActorType = "user"
	}
	record := model.AuditLog{
		ID:          uuid.New(),
		ActorID:     entry.ActorID,
		ActorType:   entry.ActorType,
		GuildID:     entry.GuildID,
		Action:      entry.Action,
		TargetType:  entry.TargetType,
		TargetID:    entry.TargetID,
		Detail:      detail,
		Reversible:  reversible,
		UndoStatus:  undoStatus,
		BeforeState: beforeJSON,
		AfterState:  afterJSON,
		UndoOfID:    entry.UndoOfID,
	}
	if err := db.Create(&record).Error; err != nil {
		return model.AuditLog{}, err
	}
	return record, nil
}

func resolveReversibility(entry Entry, before map[string]any) (reversible bool, status string) {
	// 撤销动作本身不可再撤。
	if entry.UndoOfID != nil || entry.Action == "audit.undo" {
		return false, model.AuditUndoIrreversible
	}
	if entry.Irreversible {
		return false, model.AuditUndoIrreversible
	}
	if entry.Reversible {
		return true, model.AuditUndoAvailable
	}
	info, ok := Lookup(entry.Action)
	if !ok {
		// 未知 action：有 before 快照则标可逆，否则 none（兼容旧调用）。
		if before != nil && len(before) > 0 {
			return true, model.AuditUndoAvailable
		}
		return false, model.AuditUndoNone
	}
	if info.Irreversible {
		return false, model.AuditUndoIrreversible
	}
	if info.Reversible {
		// 可逆目录项：有 before 或 handler 可自洽（如 ban 用 target_id）即可 available。
		return true, model.AuditUndoAvailable
	}
	return false, model.AuditUndoNone
}

func asMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	default:
		// json 数字等嵌套已是 map[string]any；其它类型放弃。
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, false
		}
		return out, true
	}
}

// ParseState 解析 BeforeState/AfterState JSON。
func ParseState(raw string) map[string]any {
	if raw == "" || raw == "{}" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]any{}
	}
	return out
}

// ParseDetail 解析 Detail JSON。
func ParseDetail(raw string) map[string]any {
	return ParseState(raw)
}
