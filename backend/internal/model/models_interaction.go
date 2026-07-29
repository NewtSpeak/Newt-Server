package model

import (
	"time"

	"github.com/google/uuid"
)

// ================= 消息交互领域模型（bot 交互按钮，设计文档 2026-07-26）=================

// MessageInteraction 按钮点击交互记录：审计（谁点了什么）、防重放去重、
// token 化回应（bot 崩溃重启后 TTL 内仍可凭 token 回应）、GC 过期。
//   - ID 为消息域雪花 ID（与 Message 同生成器，可按时间排序）；
//   - TokenHash 回应令牌 SHA-256（明文 newtint_… 仅在 INTERACTION_CREATE 事件中下发一次）；
//   - 状态机 PENDING →(ack)→ ACKED →(reply|update)→ RESPONDED；超时未回应由 GC 置 EXPIRED。
type MessageInteraction struct {
	ID        int64     `gorm:"primaryKey;autoIncrement:false" json:"id,string"`
	GuildID   uuid.UUID `gorm:"type:uuid;not null" json:"guild_id"` // 私信为 uuid.Nil
	ChannelID uuid.UUID `gorm:"type:uuid;not null;index:idx_interaction_channel" json:"channel_id"`
	MessageID int64     `gorm:"not null;index:idx_interaction_message" json:"message_id,string"`
	CustomID  string    `gorm:"size:64;not null" json:"custom_id"`
	// UserID 点击者；BotUserID 原消息作者（bot 用户），callback 时校验归属。
	UserID    uuid.UUID `gorm:"type:uuid;not null;index:idx_interaction_user" json:"user_id"`
	BotUserID uuid.UUID `gorm:"type:uuid;not null;index:idx_interaction_bot" json:"bot_user_id"`
	TokenHash string    `gorm:"size:64;not null" json:"-"`
	Status    string    `gorm:"size:16;not null;default:'PENDING'" json:"status"`
	CreatedAt time.Time `gorm:"index:idx_interaction_created" json:"created_at"`
	// RespondedAt 首次 reply/update 的时间；ExpiresAt 创建 +15min，过期后 callback 一律 410。
	RespondedAt *time.Time `json:"responded_at,omitempty"`
	ExpiresAt   time.Time  `gorm:"not null;index:idx_interaction_expires" json:"expires_at"`
}

// MessageInteraction 状态机取值。
const (
	InteractionPending   = "PENDING"
	InteractionAcked     = "ACKED"
	InteractionResponded = "RESPONDED"
	InteractionExpired   = "EXPIRED"
)

func init() {
	Register(&MessageInteraction{})
}
