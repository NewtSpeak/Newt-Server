package model

import (
	"time"

	"github.com/google/uuid"
)

// AuditLog 全局审计流水：enrollment、节点启停、迁移、Restriction、配额调整等敏感操作必须记录。
type AuditLog struct {
	ID         uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	ActorID    *uuid.UUID `gorm:"type:uuid;index" json:"actor_id"`                // 为空表示系统自动动作
	ActorType  string     `gorm:"size:32;not null;default:'user'" json:"actor_type"` // user / system_admin / guild_admin / auto / node
	GuildID    *uuid.UUID `gorm:"type:uuid;index" json:"guild_id"`
	Action     string     `gorm:"size:64;not null;index" json:"action"`
	TargetType string     `gorm:"size:32;not null" json:"target_type"`
	TargetID   string     `gorm:"size:64;not null;index" json:"target_id"`
	Detail     string     `gorm:"type:jsonb;not null;default:'{}'" json:"detail"`
	CreatedAt  time.Time  `gorm:"index" json:"created_at"`
}

func init() { Register(&AuditLog{}) }
