package model

import (
	"time"

	"github.com/google/uuid"
)

// 审计撤销状态（UndoStatus）。
const (
	AuditUndoNone         = "none"         // 历史兼容 / 未声明
	AuditUndoAvailable    = "available"    // 可撤销
	AuditUndoUndone       = "undone"       // 已被撤销
	AuditUndoBlocked      = "blocked"      // 当前不可撤销（冲突等）
	AuditUndoIrreversible = "irreversible" // 明确不可逆
)

// AuditLog 全局审计流水：enrollment、节点启停、迁移、Restriction、配额调整等敏感操作必须记录。
// 可撤销扩展：BeforeState/AfterState 存机器可恢复快照；UndoStatus 驱动前端按钮；
// 撤销时不改写原 Detail，仅标记 undone_* 并追加一条 UndoOfID 指向原日志的补偿记录。
type AuditLog struct {
	ID         uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	ActorID    *uuid.UUID `gorm:"type:uuid;index" json:"actor_id"`                     // 为空表示系统自动动作
	ActorType  string     `gorm:"size:32;not null;default:'user'" json:"actor_type"`   // user / system_admin / guild_admin / auto / node
	GuildID    *uuid.UUID `gorm:"type:uuid;index" json:"guild_id"`
	Action     string     `gorm:"size:64;not null;index" json:"action"`
	TargetType string     `gorm:"size:32;not null" json:"target_type"`
	TargetID   string     `gorm:"size:64;not null;index" json:"target_id"`
	Detail     string     `gorm:"type:jsonb;not null;default:'{}'" json:"detail"`
	CreatedAt  time.Time  `gorm:"index" json:"created_at"`

	// —— 可撤销扩展 ——
	Reversible  bool       `gorm:"not null;default:false" json:"reversible"`
	UndoStatus  string     `gorm:"size:16;not null;default:'none';index" json:"undo_status"`
	BeforeState string     `gorm:"type:jsonb;not null;default:'{}'" json:"before_state"`
	AfterState  string     `gorm:"type:jsonb;not null;default:'{}'" json:"after_state"`
	UndoOfID    *uuid.UUID `gorm:"type:uuid;index" json:"undo_of_id"`   // 本条若是撤销动作，指向被撤销的原日志
	UndoneByID  *uuid.UUID `gorm:"type:uuid" json:"undone_by_id"`       // 原日志被哪条撤销日志撤销
	UndoneAt    *time.Time `json:"undone_at"`
}

func init() { Register(&AuditLog{}) }
