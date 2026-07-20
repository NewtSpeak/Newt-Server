package model

import (
	"time"

	"github.com/google/uuid"
)

// 语音频道舞台模式（docs 11 §2.1）。
const (
	StageModeFree  = "FREE_DISCUSSION"
	StageModeStage = "STAGE"
)

// 申请队列来源（docs 11 §5.1）。
const (
	StageQueueSourceUserApply = "USER_APPLY"
	StageQueueSourceCapacity  = "CAPACITY_QUEUE"
	StageQueueSourceAdmin     = "ADMIN"
)

// 屏幕共享占坑状态（docs 14 AZ.4）。
const (
	ScreenSlotReserved = "RESERVED"
	ScreenSlotActive   = "ACTIVE"
)

// StageChannelConfig 语音频道舞台配置（docs 11 §2.2）。
// 无记录时按默认值处理：FREE_DISCUSSION / max_speakers=20 / 开申请 / 协管可改模式。
type StageChannelConfig struct {
	ChannelID             uuid.UUID `gorm:"type:uuid;primaryKey" json:"channel_id"`
	GuildID               uuid.UUID `gorm:"type:uuid;not null;index:idx_stage_config_guild" json:"guild_id"`
	Mode                  string    `gorm:"size:20;not null;default:'FREE_DISCUSSION'" json:"mode"`
	MaxSpeakers           int       `gorm:"not null;default:20" json:"max_speakers"` // 1..50，硬顶 50（docs 11 AA.1）
	RequestToSpeakEnabled bool      `gorm:"not null;default:true" json:"request_to_speak_enabled"`
	AllowCoModChangeMode  bool      `gorm:"not null;default:true" json:"allow_co_mod_change_mode"` // docs 11 Y.2
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// StageCoModerator 频道协管任命列表（docs 11 §7.2：任命 + 权限节点交集生效）。
type StageCoModerator struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID   uuid.UUID `gorm:"type:uuid;not null;index:idx_stage_comod_guild" json:"guild_id"`
	ChannelID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_stage_comod_channel_user" json:"channel_id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_stage_comod_channel_user" json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

// StageQueueEntry 上麦申请队列（docs 11 §5.1）：FIFO，按 requested_at 升序即队列顺序。
type StageQueueEntry struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID        uuid.UUID  `gorm:"type:uuid;not null;index:idx_stage_queue_guild" json:"guild_id"`
	ChannelID      uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_stage_queue_channel_user" json:"channel_id"`
	UserID         uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_stage_queue_channel_user" json:"user_id"`
	Source         string     `gorm:"size:20;not null" json:"source"` // USER_APPLY / CAPACITY_QUEUE / ADMIN
	RequestedAt    time.Time  `gorm:"not null;index:idx_stage_queue_requested" json:"requested_at"`
	DisconnectedAt *time.Time `gorm:"index:idx_stage_queue_disconnected" json:"disconnected_at"` // 断线 60s 保留队位（docs 11 AC.3）
	CreatedAt      time.Time  `json:"created_at"`
}

// StageSpeaker 台上 SPEAKER 席位（docs 11 §4）。granted_at 用于「更久者优先留台」类裁剪排序。
type StageSpeaker struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID        uuid.UUID  `gorm:"type:uuid;not null;index:idx_stage_speaker_guild" json:"guild_id"`
	ChannelID      uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_stage_speaker_channel_user" json:"channel_id"`
	UserID         uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_stage_speaker_channel_user" json:"user_id"`
	GrantedAt      time.Time  `gorm:"not null;index:idx_stage_speaker_granted" json:"granted_at"`
	DisconnectedAt *time.Time `gorm:"index:idx_stage_speaker_disconnected" json:"disconnected_at"` // 断线 60s 保留席位（docs 11 AC.3）
	CreatedAt      time.Time  `json:"created_at"`
}

// StageCapacityMute 容量自动禁说标记（docs 11 §3，来源 CAPACITY_QUEUE，与 Restriction/server_mute 分来源）。
type StageCapacityMute struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID   uuid.UUID `gorm:"type:uuid;not null;index:idx_stage_capmute_guild" json:"guild_id"`
	ChannelID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_stage_capmute_channel_user" json:"channel_id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_stage_capmute_channel_user" json:"user_id"`
	MutedAt   time.Time `gorm:"not null;index:idx_stage_capmute_muted" json:"muted_at"` // FIFO 解除按此排序
}

// ScreenSlot 屏幕共享占坑（docs 14 §4）：RESERVED 预留 → 发布确认 ACTIVE；超时/失败释放，防超卖。
type ScreenSlot struct {
	ID                   uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID              uuid.UUID  `gorm:"type:uuid;not null;index:idx_screen_slot_guild" json:"guild_id"`
	ChannelID            uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_screen_slot_channel_user" json:"channel_id"`
	UserID               uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_screen_slot_channel_user" json:"user_id"` // 每用户同时 1 路（docs 14 AX.4）
	State                string     `gorm:"size:16;not null" json:"state"`                                              // RESERVED / ACTIVE
	QualityTier          string     `gorm:"size:8;not null" json:"quality_tier"`                                        // 480p / 720p / 1080p
	ReservationExpiresAt *time.Time `gorm:"index:idx_screen_slot_reserve_exp" json:"reservation_expires_at"`            // RESERVED 超时释放
	StartedAt            *time.Time `json:"started_at"`
	NodeID               *uuid.UUID `gorm:"type:uuid" json:"node_id"`
	DisconnectedAt       *time.Time `gorm:"index:idx_screen_slot_disconnected" json:"disconnected_at"` // 断线 60s 保留配额（docs 14 BB.4）
	CreatedAt            time.Time  `json:"created_at"`
}

// ScreenGuildQuota 每服屏幕并发基准上限（docs 14 AY.1：新服默认 3，系统管可调）。
type ScreenGuildQuota struct {
	GuildID              uuid.UUID `gorm:"type:uuid;primaryKey" json:"guild_id"`
	MaxConcurrentScreens int       `gorm:"not null;default:3" json:"max_concurrent_screens"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// ScreenChannelQuota 每频道屏幕并发上限（docs 14 AY.2：默认 2）。
type ScreenChannelQuota struct {
	ChannelID            uuid.UUID `gorm:"type:uuid;primaryKey" json:"channel_id"`
	GuildID              uuid.UUID `gorm:"type:uuid;not null;index:idx_screen_chquota_guild" json:"guild_id"`
	MaxConcurrentScreens int       `gorm:"not null;default:2" json:"max_concurrent_screens"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// ScreenPlatformSetting 平台级屏幕共享设置（单行，ID 固定为 1）。
// 动态降额开关与成本权重（docs 14 AY.4–AY.6 / BD），权重初值待压测标定（BD.2）。
type ScreenPlatformSetting struct {
	ID              int       `gorm:"primaryKey" json:"id"`
	DynamicEnabled  bool      `gorm:"not null;default:true" json:"dynamic_screen_quota_enabled"`
	GentleEndOldest bool      `gorm:"not null;default:true" json:"gentle_end_oldest"` // 动态超限时结束最早开启的共享（docs 14 AZ.3②，默认开）
	DefaultQuality  string    `gorm:"size:8;not null;default:'720p'" json:"default_quality"`
	MaxQuality      string    `gorm:"size:8;not null;default:'1080p'" json:"max_quality"` // 平台清晰度天花板（docs 14 BA.2）
	Weight480p      float64   `gorm:"not null;default:1.0" json:"weight_480p"`
	Weight720p      float64   `gorm:"not null;default:1.5" json:"weight_720p"`
	Weight1080p     float64   `gorm:"not null;default:2.5" json:"weight_1080p"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func init() {
	Register(
		&StageChannelConfig{}, &StageCoModerator{}, &StageQueueEntry{}, &StageSpeaker{}, &StageCapacityMute{},
		&ScreenSlot{}, &ScreenGuildQuota{}, &ScreenChannelQuota{}, &ScreenPlatformSetting{},
	)
}
