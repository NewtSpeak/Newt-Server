package model

import (
	"time"

	"github.com/google/uuid"
)

// VoiceState 对齐 docs/设计讨论/2026-07-20-05-进房信令时序.md §2.2。
// 同一 (guild_id, user_id) 同时只允许一个语音频道；ChannelID 为空表示不在任何语音频道。
// 该结构是语音编排、舞台、屏幕共享等模块之间的共享契约，扩展状态请放各自领域表，不要往这里加列。
type VoiceState struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID        uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_voice_state_guild_user" json:"guild_id"`
	UserID         uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_voice_state_guild_user" json:"user_id"`
	ChannelID      *uuid.UUID `gorm:"type:uuid;index" json:"channel_id"`
	NodeID         *uuid.UUID `gorm:"type:uuid;index" json:"node_id"`
	RoomID         *uuid.UUID `gorm:"type:uuid" json:"room_id"` // logical_room_id = channel_id（docs 08 A.1）
	VoiceSessionID *uuid.UUID `gorm:"type:uuid" json:"voice_session_id"`
	SelfMute       bool       `gorm:"not null;default:false" json:"self_mute"`
	SelfDeaf       bool       `gorm:"not null;default:false" json:"self_deaf"`
	ServerMute     bool       `gorm:"not null;default:false" json:"server_mute"`
	ServerDeaf     bool       `gorm:"not null;default:false" json:"server_deaf"`
	Connected      bool       `gorm:"not null;default:false" json:"connected"`
	JoinedAt       *time.Time `json:"joined_at"` // 进入当前频道的时间，容量禁说队列按此排序（docs 11 Z.3）
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func init() { Register(&VoiceState{}) }
