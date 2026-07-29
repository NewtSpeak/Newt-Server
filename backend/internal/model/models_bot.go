package model

import (
	"time"

	"github.com/google/uuid"
)

// ================= 机器人领域模型（bot 专项）=================
//
// 设计（见 internal/botapi 包注释）：bot 复用 User(IsBot=true)+Member+Role 的既有
// RBAC 权限体系——「安装到服」= 为 bot 用户创建 Member，「权限赋予」= 给该 Member
// 绑定角色 / 配频道覆盖，与人类成员完全同构，无需重写权限计算。
// 本文件只补充 bot 特有的实体：应用档案（Bot）与长期访问令牌（BotToken）。

// Bot 机器人应用档案：一个 Bot 恒绑定一个 IsBot=true 的 User（消息作者、
// 语音参与者、权限主体均为该 User）。
type Bot struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	// UserID 关联的 bot 用户（users.is_bot=true），一一对应。
	UserID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_bot_user" json:"user_id"`
	// OwnerUserID 创建者（系统管理员或服主/持 MANAGE_BOTS 的成员）。
	OwnerUserID uuid.UUID `gorm:"type:uuid;not null;index:idx_bot_owner" json:"owner_user_id"`
	// HomeGuildID 归属服务器：非空表示「服级机器人」，仅可安装在该服，
	// 由服主/MANAGE_BOTS 在服务器设置中创建与管理；空 = 平台级机器人
	// （仅系统管理员创建，可安装到多服）。
	HomeGuildID *uuid.UUID `gorm:"type:uuid;index:idx_bot_home_guild" json:"home_guild_id,omitempty"`
	Name        string     `gorm:"size:64;not null" json:"name"`
	Description string     `gorm:"size:512;not null;default:''" json:"description"`
	AvatarURL   string     `gorm:"size:512;not null;default:''" json:"avatar_url"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// BotToken 机器人长期访问令牌（非密码登录）：
//   - 明文形如 newtbot_<base64url 32B>，仅创建响应返回一次；
//   - DB 只存 SHA-256（与 RefreshToken 同策略），Prefix 存前几位供后台辨识；
//   - 可设过期时间，可随时吊销（RevokedAt），LastUsedAt 用于闲置令牌治理。
type BotToken struct {
	ID         uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	BotID      uuid.UUID  `gorm:"type:uuid;not null;index:idx_bot_token_bot" json:"bot_id"`
	Name       string     `gorm:"size:64;not null;default:''" json:"name"`
	TokenHash  string     `gorm:"size:64;uniqueIndex:idx_bot_token_hash;not null" json:"-"`
	Prefix     string     `gorm:"size:20;not null;default:''" json:"prefix"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

func init() {
	Register(&Bot{}, &BotToken{})
}
