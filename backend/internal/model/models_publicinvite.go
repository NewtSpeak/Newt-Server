package model

import (
	"time"

	"github.com/google/uuid"
)

// InviteLandingConfig 每服一份的邀请落地页配置：未创建记录时视为「启用 + 空简介」。
type InviteLandingConfig struct {
	ID      uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_invite_landing_guild" json:"guild_id"`
	// Description 服务器公开简介（落地页展示，未登录可见，注意内容脱敏）。
	Description string `gorm:"type:text;not null;default:''" json:"description"`
	// Enabled 关闭后公开落地页对该服的邀请返回 404（邀请码本身仍可在客户端内使用）。
	Enabled bool `gorm:"not null;default:true" json:"enabled"`
	// AutoDeepLink 打开落地页时自动尝试唤起桌面客户端（newtspeak:// 深链）。
	AutoDeepLink bool      `gorm:"not null;default:true" json:"auto_deep_link"`
	UpdatedBy    uuid.UUID `gorm:"type:uuid" json:"updated_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// InviteNoticeKind 落地页内容块类型：公告 / 注意事项 / 协议。
type InviteNoticeKind string

const (
	NoticeAnnouncement InviteNoticeKind = "ANNOUNCEMENT"
	NoticeNotice       InviteNoticeKind = "NOTICE"
	NoticeAgreement    InviteNoticeKind = "AGREEMENT"
)

// InviteNotice 落地页内容块，支持同类多条（对齐 Discord 全服协议/公告体验）。
type InviteNotice struct {
	ID       uuid.UUID        `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID  uuid.UUID        `gorm:"type:uuid;not null;index:idx_invite_notice_guild" json:"guild_id"`
	Kind     InviteNoticeKind `gorm:"size:20;not null" json:"kind"`
	Title    string           `gorm:"size:200;not null" json:"title"`
	Body     string           `gorm:"type:text;not null;default:''" json:"body"`
	Position int              `gorm:"not null;default:0" json:"position"`
	Enabled  bool             `gorm:"not null;default:true" json:"enabled"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// InvitePortalConfig 全局（单行，ID 恒为 1）下载渠道与深链配置：
// 落地页下载引导与客户端唤起协议均取自本表，由系统管理员维护。
type InvitePortalConfig struct {
	ID int `gorm:"primaryKey" json:"-"`
	// AppName 落地页展示的产品名。
	AppName string `gorm:"size:64;not null;default:'NewtSpeak'" json:"app_name"`
	// DeepLinkScheme 客户端自定义协议名（不含 ://），深链形如 newtspeak://invite?...
	DeepLinkScheme string `gorm:"size:32;not null;default:'newtspeak'" json:"deep_link_scheme"`
	WindowsURL     string `gorm:"size:512;not null;default:''" json:"windows_url"`
	MacosURL       string `gorm:"size:512;not null;default:''" json:"macos_url"`
	LinuxURL       string `gorm:"size:512;not null;default:''" json:"linux_url"`
	AndroidURL     string `gorm:"size:512;not null;default:''" json:"android_url"`
	IosURL         string `gorm:"size:512;not null;default:''" json:"ios_url"`
	WebsiteURL     string `gorm:"size:512;not null;default:''" json:"website_url"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func init() { Register(&InviteLandingConfig{}, &InviteNotice{}, &InvitePortalConfig{}) }
