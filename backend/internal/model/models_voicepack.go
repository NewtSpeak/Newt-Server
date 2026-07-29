package model

import (
	"time"

	"github.com/google/uuid"
)

// ================= 入场语音包完整模型（Newt-Desktop docs 12、07 专项 5A）=================
//
// 服级单条 audio_url 配置（GuildVoicePackConfig，models_message.go）继续保留，
// 作为「服级默认语音包」与触发场景/范围/开关的载体；本文件补充完整的包模型：
// 包 CRUD（服管）、用户按服选包、RARE 稀有包按身份组授权（docs 12 FR-09~FR-13）。

// VoicePackKind 语音包稀有度（docs 12 5A.4：普通包全员可用，稀有包按角色授权；
// 「资产」授权体系未定义，本期仅实现角色授权）。
type VoicePackKind string

const (
	VoicePackStandard VoicePackKind = "STANDARD"
	VoicePackRare     VoicePackKind = "RARE"
)

// VoicePack 服级语音包（docs 12 FR-13 服管 CRUD）。
//   - AudioURL 为公开资产路径（/public-assets/voicepacks/...，文件名带纳秒版本号不可变）；
//     经音频上传端点写入，创建时可为空（未上传音频的包不会被触发播放）。
//   - AllowedRoleIDs 仅 RARE 包生效：持有其中任一身份组的成员方可选用；
//     失去身份组后已选中的 RARE 包在触发时自动失效回退（docs 12 US-2 / FR-12）。
type VoicePack struct {
	ID      uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	GuildID uuid.UUID `gorm:"type:uuid;not null;index:idx_voice_pack_guild" json:"guild_id"`
	Name    string    `gorm:"size:100;not null" json:"name"`
	// AudioURL 客户端直接 GET 的短音频 URL（docs 12 FR-02，不经 SFU）。
	AudioURL string `gorm:"size:1024;not null;default:''" json:"audio_url"`
	// DurationMS 音频时长（毫秒）：客户端上传时自报，服务端不强校验（docs 12 §6.1）。
	DurationMS int `gorm:"not null;default:0" json:"duration_ms"`
	// SizeBytes 音频文件大小（服务端落盘实测）。
	SizeBytes      int64         `gorm:"not null;default:0" json:"size_bytes"`
	Kind           VoicePackKind `gorm:"size:16;not null;default:'STANDARD'" json:"kind"`
	AllowedRoleIDs UUIDList      `gorm:"type:jsonb;not null;default:'[]'" json:"allowed_role_ids"`
	Enabled        bool          `gorm:"not null;default:true" json:"enabled"`
	CreatedBy      uuid.UUID     `gorm:"type:uuid;not null" json:"created_by"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// VoicePackSelection 用户在某服选中的语音包（docs 12 FR-09/FR-12：按服维度，一人一服一包）。
type VoicePackSelection struct {
	UserID    uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	GuildID   uuid.UUID `gorm:"type:uuid;primaryKey" json:"guild_id"`
	PackID    uuid.UUID `gorm:"type:uuid;not null;index:idx_voice_pack_selection_pack" json:"pack_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func init() { Register(&VoicePack{}, &VoicePackSelection{}) }
