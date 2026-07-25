package model

import (
	"time"

	"github.com/google/uuid"
)

// OAuth device / grant 状态。
const (
	OAuthDevicePending  = "pending"
	OAuthDeviceApproved = "approved"
	OAuthDeviceDenied   = "denied"
	OAuthDeviceConsumed = "consumed"
	OAuthDeviceExpired  = "expired"
)

// OAuthDeviceCode RFC 8628 设备授权码。
// DeviceCode 明文仅发给 CLI；库内只存 hash。UserCode 给人阅读（大写易读字符）。
type OAuthDeviceCode struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	DeviceCodeHash string     `gorm:"size:64;uniqueIndex;not null" json:"-"`
	UserCode       string     `gorm:"size:16;uniqueIndex;not null" json:"user_code"`
	ClientID       string     `gorm:"size:64;not null;index" json:"client_id"`
	Scope          string     `gorm:"size:512;not null;default:''" json:"scope"`
	Status         string     `gorm:"size:16;not null;default:'pending';index" json:"status"`
	UserID         *uuid.UUID `gorm:"type:uuid;index" json:"user_id,omitempty"`
	// GrantedScope 用户实际同意的 scope（可小于请求 scope）。
	GrantedScope string     `gorm:"size:512;not null;default:''" json:"granted_scope,omitempty"`
	Interval     int        `gorm:"not null;default:5" json:"interval"`
	ExpiresAt    time.Time  `gorm:"not null;index" json:"expires_at"`
	ApprovedAt   *time.Time `json:"approved_at,omitempty"`
	LastPollAt   *time.Time `json:"last_poll_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// OAuthAuthCode Authorization Code + PKCE 授权码（一次性）。
type OAuthAuthCode struct {
	ID                  uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	CodeHash            string    `gorm:"size:64;uniqueIndex;not null" json:"-"`
	ClientID            string    `gorm:"size:64;not null;index" json:"client_id"`
	UserID              uuid.UUID `gorm:"type:uuid;not null;index" json:"user_id"`
	RedirectURI         string    `gorm:"size:512;not null" json:"redirect_uri"`
	Scope               string    `gorm:"size:512;not null;default:''" json:"scope"`
	CodeChallenge       string    `gorm:"size:128;not null" json:"-"`
	CodeChallengeMethod string    `gorm:"size:16;not null;default:'S256'" json:"-"`
	ExpiresAt           time.Time `gorm:"not null;index" json:"expires_at"`
	UsedAt              *time.Time `json:"used_at,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

func init() {
	Register(&OAuthDeviceCode{}, &OAuthAuthCode{})
}
