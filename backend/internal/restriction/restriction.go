// Package restriction 多维限制（docs 12）。
// 本文件是稳定接口层：perms/语音/消息模块通过 Mask / Denies 消费；
// 真正的模型、API、过期扫描由本包的实现文件（Restriction 专项）补齐，并在 Register 中调用 SetService。
package restriction

import (
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

// Scope 限制作用域（docs 12 §2.1）。
type Scope string

const (
	ScopeTextChannel  Scope = "TEXT_CHANNEL"
	ScopeVoiceChannel Scope = "VOICE_CHANNEL"
	ScopeGuildAllText Scope = "GUILD_ALL_TEXT"
	ScopeGuildAllVoice Scope = "GUILD_ALL_VOICE"
)

// Kind 记录类型。
type Kind string

const (
	KindSanction   Kind = "SANCTION"
	KindChannelBan Kind = "CHANNEL_BAN"
)

// DenyFlags 四个可组合的限制维度。
type DenyFlags struct {
	ViewText    bool `json:"view_text"`
	SendText    bool `json:"send_text"`
	ListenVoice bool `json:"listen_voice"`
	SpeakVoice  bool `json:"speak_voice"`
}

// Service 供其他模块消费的只读裁决接口。实现必须「只收紧，不放宽」。
type Service interface {
	// Mask 在 RBAC 计算结果上应用生效中的限制；channel 为 nil 表示服务器级。
	Mask(bits rbac.Permission, userID, guildID uuid.UUID, channel *model.Channel) rbac.Permission
	// Denies 聚合某用户在某频道（或服务器级语音/文字）当前被禁的维度并集。
	Denies(userID, guildID uuid.UUID, channelID *uuid.UUID, channelType model.ChannelType) DenyFlags
}

var current Service = noopService{}

// SetService 由实现方在 Register 时注入。
func SetService(service Service) { current = service }

// Current 返回当前服务实现。
func Current() Service { return current }

// Mask 便捷入口。
func Mask(bits rbac.Permission, userID, guildID uuid.UUID, channel *model.Channel) rbac.Permission {
	return current.Mask(bits, userID, guildID, channel)
}

// Denies 便捷入口。
func Denies(userID, guildID uuid.UUID, channelID *uuid.UUID, channelType model.ChannelType) DenyFlags {
	return current.Denies(userID, guildID, channelID, channelType)
}

type noopService struct{}

func (noopService) Mask(bits rbac.Permission, _, _ uuid.UUID, _ *model.Channel) rbac.Permission {
	return bits
}
func (noopService) Denies(uuid.UUID, uuid.UUID, *uuid.UUID, model.ChannelType) DenyFlags {
	return DenyFlags{}
}
