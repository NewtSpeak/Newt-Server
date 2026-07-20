package model

import (
	"time"

	"github.com/google/uuid"
)

// Restriction 多维限制记录（docs 12 §2.1）。
//   - 一条记录可含多个 deny 维（AG.2）；长期频道 ban 与临时制裁同表（AG.3），kind 区分。
//   - ExpiresAt 为空表示长期（仅 CHANNEL_BAN 或系统管特许，API 层校验）。
//   - active 由 LiftedAt / ExpiresAt 推导，不单独落列。
//   - ExpiredNotifiedAt 是定时扫描的去重标记：过期 lift 事件只推一次（AN.2）。
type Restriction struct {
	ID                uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID           uuid.UUID  `gorm:"type:uuid;not null;index:idx_restriction_guild_target" json:"guild_id"`
	TargetUserID      uuid.UUID  `gorm:"type:uuid;not null;index:idx_restriction_guild_target" json:"target_user_id"`
	Scope             string     `gorm:"size:32;not null" json:"scope"`
	ChannelID         *uuid.UUID `gorm:"type:uuid;index:idx_restriction_channel" json:"channel_id"`
	DenyViewText      bool       `gorm:"not null;default:false" json:"deny_view_text"`
	DenySendText      bool       `gorm:"not null;default:false" json:"deny_send_text"`
	DenyListenVoice   bool       `gorm:"not null;default:false" json:"deny_listen_voice"`
	DenySpeakVoice    bool       `gorm:"not null;default:false" json:"deny_speak_voice"`
	Kind              string     `gorm:"size:32;not null" json:"kind"`
	Reason            string     `gorm:"size:512;not null" json:"reason"`
	ExpiresAt         *time.Time `gorm:"index:idx_restriction_expires" json:"expires_at"`
	CreatedBy         uuid.UUID  `gorm:"type:uuid;not null" json:"created_by"`
	LiftedAt          *time.Time `json:"lifted_at"`
	LiftedBy          *uuid.UUID `gorm:"type:uuid" json:"lifted_by"`
	ExpiredNotifiedAt *time.Time `json:"-"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// ActiveAt 判断记录在给定时刻是否生效（惰性过期：查询时按此过滤，AN.1）。
func (r Restriction) ActiveAt(now time.Time) bool {
	return r.LiftedAt == nil && (r.ExpiresAt == nil || r.ExpiresAt.After(now))
}

func init() { Register(&Restriction{}) }
