// Package audit 提供最小审计写入助手；敏感操作（enrollment、启停调度、迁移、Restriction、配额调整等）必须调用。
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
}

// Log 尽力写入审计记录；失败仅记日志，不阻断业务主路径。
func Log(db *gorm.DB, entry Entry) {
	detail := "{}"
	if entry.Detail != nil {
		if raw, err := json.Marshal(entry.Detail); err == nil {
			detail = string(raw)
		}
	}
	if entry.ActorType == "" {
		entry.ActorType = "user"
	}
	record := model.AuditLog{
		ID:         uuid.New(),
		ActorID:    entry.ActorID,
		ActorType:  entry.ActorType,
		GuildID:    entry.GuildID,
		Action:     entry.Action,
		TargetType: entry.TargetType,
		TargetID:   entry.TargetID,
		Detail:     detail,
	}
	if err := db.Create(&record).Error; err != nil {
		log.Printf("audit: 写入审计失败 action=%s err=%v", entry.Action, err)
	}
}
