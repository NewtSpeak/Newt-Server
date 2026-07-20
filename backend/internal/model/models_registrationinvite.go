package model

import (
	"time"

	"github.com/google/uuid"
)

// RegistrationInvite 平台注册邀请短码（系统管理员签发）：凭码注册可绕过用户端
// 注册开关（client_signup_enabled），用于「关闭公开注册、仅凭邀请注册」的部署形态。
// ExpiresAt 为空表示不过期；MaxUses 为 0 表示不限次数（1 即一次性邀请）；
// RevokedAt 非空表示已被管理员软撤销（保留记录供列表回溯）。
type RegistrationInvite struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	Code      string     `gorm:"size:16;not null;uniqueIndex:idx_registration_invite_code" json:"code"`
	CreatedBy uuid.UUID  `gorm:"type:uuid;not null" json:"created_by"`
	ExpiresAt *time.Time `gorm:"index:idx_registration_invite_expires" json:"expires_at"`
	MaxUses   int        `gorm:"not null;default:0" json:"max_uses"`
	Uses      int        `gorm:"not null;default:0" json:"uses"`
	RevokedAt *time.Time `json:"revoked_at"`
	CreatedAt time.Time  `json:"created_at"`
}

func init() { Register(&RegistrationInvite{}) }
