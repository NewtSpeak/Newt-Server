package model

import (
	"time"

	"github.com/google/uuid"
)

// ================= 系统管理员临场 / 音频审计（adminpresence 专项）=================

// PlatformAuditConfig 平台级音频审计默认配置（单行，ID 恒为 1）。
// 频道无独立配置时继承此默认；系统管理员维护。
type PlatformAuditConfig struct {
	ID int `gorm:"primaryKey" json:"-"`
	// RecordDefault 默认是否将语音频道音频录制到主节点服务器（审计）。
	RecordDefault bool `gorm:"not null" json:"record_default"`
	// NotifyDefault 默认是否向用户提示该频道正在被审计（可关闭 = 静默审计）。
	// 注意：不加 default 标签——GORM 会把带 default 的零值 false 从 INSERT 中省略、
	// 回落 DB 默认，导致写不进 false；语义默认（缺省 true）由应用层裁决。
	NotifyDefault bool      `gorm:"not null" json:"notify_default"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ChannelAuditConfig 频道级音频审计配置：存在记录时覆盖平台默认。
type ChannelAuditConfig struct {
	ChannelID uuid.UUID `gorm:"type:uuid;primaryKey" json:"channel_id"`
	GuildID   uuid.UUID `gorm:"type:uuid;not null;index:idx_channel_audit_guild" json:"guild_id"`
	// Record 是否录制该频道音频到主节点（审计）。
	Record bool `gorm:"not null" json:"record"`
	// Notify 是否提示用户本频道被审计（false = 静默审计）。不加 default 标签，
	// 避免 GORM 省略零值 false（同 PlatformAuditConfig 说明）。
	Notify    bool      `gorm:"not null" json:"notify"`
	UpdatedBy uuid.UUID `gorm:"type:uuid" json:"updated_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AdminVoicePresence 系统管理员语音临场隐身标记（每 guild+admin 一条）。
// Hidden=true 时该管理员的语音会话不进成员列表、不广播 VOICE_STATE_UPDATE，
// 且其 Media Token 带 hidden claim（SFU 抑制其 participant_joined/left 广播）。
type AdminVoicePresence struct {
	GuildID uuid.UUID `gorm:"type:uuid;primaryKey" json:"guild_id"`
	UserID  uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	// Hidden 不加 default 标签，避免 GORM 省略零值 false。
	Hidden    bool      `gorm:"not null" json:"hidden"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AudioAuditRecord 一段被审计录制的音频（每个说话者的一条上行轨对应一条记录）。
// 由 SFU 节点录制后经 /audit-api 上传，主节点服务器落盘 + 记录元数据。
type AudioAuditRecord struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID   uuid.UUID  `gorm:"type:uuid;not null;index:idx_audit_rec_guild" json:"guild_id"`
	ChannelID uuid.UUID  `gorm:"type:uuid;not null;index:idx_audit_rec_channel" json:"channel_id"`
	UserID    uuid.UUID  `gorm:"type:uuid;not null;index:idx_audit_rec_user" json:"user_id"`
	SessionID string     `gorm:"size:64;not null;default:''" json:"session_id"`
	NodeID    string     `gorm:"size:64;not null;default:''" json:"node_id"`
	// ObjectKey 主节点本地存储相对路径（DataDir/audit 下的 .ogg）。
	ObjectKey string    `gorm:"size:255;not null" json:"-"`
	MIME      string    `gorm:"size:64;not null;default:'audio/ogg'" json:"mime"`
	Size      int64     `gorm:"not null;default:0" json:"size"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	CreatedAt time.Time `gorm:"index:idx_audit_rec_created" json:"created_at"`
}

func init() {
	Register(&PlatformAuditConfig{}, &ChannelAuditConfig{}, &AdminVoicePresence{}, &AudioAuditRecord{})
}
