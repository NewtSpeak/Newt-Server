package model

import (
	"time"

	"github.com/google/uuid"
)

// Invite 服务器邀请短码；ExpiresAt 为空表示不过期。
// MaxUses 为 0 表示不限次数；Uses 只统计真正新加入的成员（幂等重入不计数）。
type Invite struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID   uuid.UUID  `gorm:"type:uuid;not null;index:idx_moderation_invite_guild" json:"guild_id"`
	Code      string     `gorm:"size:16;not null;uniqueIndex:idx_moderation_invite_code" json:"code"`
	CreatedBy uuid.UUID  `gorm:"type:uuid;not null" json:"created_by"`
	ExpiresAt *time.Time `gorm:"index:idx_moderation_invite_expires" json:"expires_at"`
	MaxUses   int        `gorm:"not null;default:0" json:"max_uses"`
	Uses      int        `gorm:"not null;default:0" json:"uses"`
	CreatedAt time.Time  `json:"created_at"`
}

// GuildBan 服务器封禁（docs 12 AG.4 / AO.3）：移出成员并阻止凭邀请再加入。
type GuildBan struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_moderation_ban_guild_user" json:"guild_id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_moderation_ban_guild_user" json:"user_id"`
	Reason    string    `gorm:"size:512" json:"reason"`
	CreatedBy uuid.UUID `gorm:"type:uuid;not null" json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

func init() { Register(&Invite{}, &GuildBan{}) }
