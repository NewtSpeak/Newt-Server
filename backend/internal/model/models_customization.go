package model

import (
	"time"

	"github.com/google/uuid"
)

// Badge 服务器徽章定义（customization 专项）：由服管/系统管理员创建，
// 可随时分配给成员（永久 / 有效天数 / 截止日期，见 UserBadge.ExpiresAt）。
type Badge struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID     uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_badge_guild_name" json:"guild_id"`
	Name        string    `gorm:"size:64;not null;uniqueIndex:idx_badge_guild_name" json:"name"`
	Description string    `gorm:"size:255;not null;default:''" json:"description"`
	// Emoji 徽章图标（emoji 或短文本）；IconURL 可选自定义图标地址，二者取其一展示。
	Emoji   string `gorm:"size:32;not null;default:''" json:"emoji"`
	IconURL string `gorm:"size:512;not null;default:''" json:"icon_url"`
	// Color 徽章主题色（#RRGGBB），前端据此渲染徽章底色/描边。
	Color     string    `gorm:"size:32;not null;default:''" json:"color"`
	CreatedBy uuid.UUID `gorm:"type:uuid;not null" json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UserBadge 徽章授予记录：ExpiresAt 为空表示永久；到期后视为自动失效
//（查询侧过滤，无需后台任务）。同一徽章对同一用户最多一条有效记录。
type UserBadge struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	BadgeID   uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_user_badge_once" json:"badge_id"`
	UserID    uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_user_badge_once;index:idx_user_badge_user" json:"user_id"`
	GuildID   uuid.UUID  `gorm:"type:uuid;not null;index:idx_user_badge_guild" json:"guild_id"`
	GrantedBy uuid.UUID  `gorm:"type:uuid;not null" json:"granted_by"`
	ExpiresAt *time.Time `gorm:"index:idx_user_badge_expires" json:"expires_at"`
	CreatedAt time.Time  `json:"granted_at"`
}

func init() { Register(&Badge{}, &UserBadge{}) }
