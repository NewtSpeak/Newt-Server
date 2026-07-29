package message

import (
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
)

// 配置变更事件（实时同步专项）：服级/频道级配置改动即时推送给受影响成员，
// 客户端据 kind 决定失效哪块本地缓存（上传上限前置校验、语音包开关等）。

// publishGuildConfigUpdate GUILD_CONFIG_UPDATE：guild 广播。
// kind ∈ upload_limit / message_retention / voice_pack；config 为该域配置的最新视图。
func (s *service) publishGuildConfigUpdate(guildID uuid.UUID, kind string, config any) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(eventbus.Event{
		Type:    eventbus.EventGuildConfigUpdate,
		GuildID: &guildID,
		Payload: map[string]any{
			"guild_id": guildID, "kind": kind, "config": config, "event_at": time.Now().UTC(),
		},
	})
}

// publishChannelConfigUpdate CHANNEL_CONFIG_UPDATE：带 ChannelID，
// Gateway 按 VIEW_CHANNEL 可见性过滤（频道级配置不外泄给无权成员）。
func (s *service) publishChannelConfigUpdate(guildID, channelID uuid.UUID, kind string, config any) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(eventbus.Event{
		Type:      eventbus.EventChannelConfigUpdate,
		GuildID:   &guildID,
		ChannelID: &channelID,
		Payload: map[string]any{
			"guild_id": guildID, "channel_id": channelID, "kind": kind,
			"config": config, "event_at": time.Now().UTC(),
		},
	})
}

// publishVoicePackUpdate VOICE_PACK_UPDATE：语音包定义变更 guild 广播；
// targetUserIDs 非空时改为定向（用户选包变更 / 删包连带清除选择的受影响者）。
func (s *service) publishVoicePackUpdate(guildID uuid.UUID, payload map[string]any, targetUserIDs ...uuid.UUID) {
	if s.bus == nil {
		return
	}
	payload["guild_id"] = guildID
	payload["event_at"] = time.Now().UTC()
	event := eventbus.Event{
		Type:    eventbus.EventVoicePackUpdate,
		GuildID: &guildID,
		Payload: payload,
	}
	if len(targetUserIDs) > 0 {
		event.UserIDs = targetUserIDs
	}
	s.bus.Publish(event)
}
