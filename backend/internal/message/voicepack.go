package message

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/perms"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 入场语音包配置（docs 07 专项 5A，首期只做配置存取 + 事件发布函数）。
// 实际触发接线由语音模块在集成阶段完成：用户进房时由语音模块判定触发条件
//（进服首次出现 / 进指定语音频道，见 GuildVoicePackConfig.Trigger），
// 然后调用本包导出的 PlayVoicePack 完成配置裁决与事件发布（5A.3 客户端拉 URL 本地播放）。

// guildAccess 加载服级权限上下文；非成员且非系统管一律 404。
func (s *service) guildAccess(c *gin.Context) (*perms.GuildContext, bool) {
	guildID, ok := parseUUIDParam(c, "guildID")
	if !ok {
		return nil, false
	}
	ctx, err := perms.LoadGuild(s.db, s.currentUser(c), guildID)
	if err != nil {
		notFound(c)
		return nil, false
	}
	return ctx, true
}

func (s *service) guildVoicePackConfig(guildID uuid.UUID) model.GuildVoicePackConfig {
	config := model.GuildVoicePackConfig{
		GuildID: guildID,
		Scope:   model.VoicePackSameChannel,
		Trigger: model.VoicePackFirstJoin,
	}
	s.db.Where("guild_id = ?", guildID).First(&config)
	return config
}

// getGuildVoicePack GET /guilds/{gid}/voice-pack：成员可读（客户端需要知道是否播放）。
func (s *service) getGuildVoicePack(c *gin.Context) {
	ctx, ok := s.guildAccess(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, s.guildVoicePackConfig(ctx.Guild.ID))
}

type voicePackPatchRequest struct {
	Enabled  *bool   `json:"enabled"`
	AudioURL *string `json:"audio_url" binding:"omitempty,max=1024"`
	Scope    *string `json:"scope"`
	Trigger  *string `json:"trigger"`
}

// patchGuildVoicePack PATCH /guilds/{gid}/voice-pack：服管（MANAGE_GUILD）配置（5A.4）。
func (s *service) patchGuildVoicePack(c *gin.Context) {
	ctx, ok := s.guildAccess(c)
	if !ok {
		return
	}
	if !ctx.Has(rbac.ManageGuild) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "缺少管理服务器权限")
		return
	}
	var input voicePackPatchRequest
	if !bind(c, &input) {
		return
	}
	config := s.guildVoicePackConfig(ctx.Guild.ID)
	if input.Enabled != nil {
		config.Enabled = *input.Enabled
	}
	if input.AudioURL != nil {
		config.AudioURL = *input.AudioURL
	}
	if input.Scope != nil {
		scope := model.VoicePackScope(*input.Scope)
		if scope != model.VoicePackSameChannel && scope != model.VoicePackGuildOnline {
			fail(c, http.StatusBadRequest, "INVALID_SCOPE", "scope 需为 SAME_CHANNEL 或 GUILD_ONLINE")
			return
		}
		config.Scope = scope
	}
	if input.Trigger != nil {
		trigger := model.VoicePackTrigger(*input.Trigger)
		if trigger != model.VoicePackFirstJoin && trigger != model.VoicePackChannelJoin {
			fail(c, http.StatusBadRequest, "INVALID_TRIGGER", "trigger 需为 FIRST_GUILD_JOIN 或 CHANNEL_JOIN")
			return
		}
		config.Trigger = trigger
	}
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "guild_id"}},
		UpdateAll: true,
	}).Create(&config).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存语音包配置失败")
		return
	}
	actor := s.currentUser(c)
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "guild_admin", GuildID: &ctx.Guild.ID,
		Action: "voicepack.guild_config", TargetType: "guild", TargetID: ctx.Guild.ID.String(),
		Detail: map[string]any{"enabled": config.Enabled, "scope": config.Scope, "trigger": config.Trigger},
	})
	c.JSON(http.StatusOK, config)
}

// getChannelVoicePack GET /guilds/{gid}/channels/{cid}/voice-pack：频道可见即可读。
func (s *service) getChannelVoicePack(c *gin.Context) {
	ctx, ok := s.guildAccess(c)
	if !ok {
		return
	}
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	channel, _, err := ctx.ChannelPerms(s.db, channelID)
	if err != nil {
		notFound(c)
		return
	}
	config := model.ChannelVoicePackConfig{ChannelID: channel.ID, GuildID: ctx.Guild.ID, Allowed: true}
	s.db.Where("channel_id = ?", channel.ID).First(&config)
	c.JSON(http.StatusOK, config)
}

type channelVoicePackRequest struct {
	Allowed bool `json:"allowed"`
}

// putChannelVoicePack PUT /guilds/{gid}/channels/{cid}/voice-pack：
// 子频道管理员（该频道 MANAGE_CHANNELS）开关本频道是否允许播放（5A.1b）。
func (s *service) putChannelVoicePack(c *gin.Context) {
	ctx, ok := s.guildAccess(c)
	if !ok {
		return
	}
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	channel, bits, err := ctx.ChannelPerms(s.db, channelID)
	if err != nil {
		notFound(c)
		return
	}
	if !rbac.Has(bits, rbac.ManageChannels) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "缺少管理频道权限")
		return
	}
	var input channelVoicePackRequest
	if !bind(c, &input) {
		return
	}
	config := model.ChannelVoicePackConfig{ChannelID: channel.ID, GuildID: ctx.Guild.ID, Allowed: input.Allowed}
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"allowed", "updated_at"}),
	}).Create(&config).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存频道语音包开关失败")
		return
	}
	actor := s.currentUser(c)
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, GuildID: &ctx.Guild.ID,
		Action: "voicepack.channel_toggle", TargetType: "channel", TargetID: channel.ID.String(),
		Detail: map[string]any{"allowed": input.Allowed},
	})
	c.JSON(http.StatusOK, config)
}

// VoicePackPlayPayload EventVoicePackPlay 的载荷：客户端收到后拉取 audio_url 本地播放（5A.3）。
type VoicePackPlayPayload struct {
	GuildID   uuid.UUID            `json:"guild_id"`
	ChannelID uuid.UUID            `json:"channel_id"`
	UserID    uuid.UUID            `json:"user_id"`
	AudioURL  string               `json:"audio_url"`
	Scope     model.VoicePackScope `json:"scope"`
}

// PlayVoicePack 供语音模块在用户进房且触发条件满足时调用（触发时机判定由调用方负责，
// 参考 GuildVoicePackConfig.Trigger）。本函数完成配置裁决（服级开关 + 频道级开关）并
// 发布 EventVoicePackPlay；返回是否实际发布。
//   - Scope=SAME_CHANNEL：事件带 ChannelID，Gateway 按频道在房用户下发；
//   - Scope=GUILD_ONLINE：事件不带 ChannelID，Gateway 按服在线广播（可见性过滤仍生效）。
func PlayVoicePack(db *gorm.DB, bus *eventbus.Bus, guildID, channelID, userID uuid.UUID) bool {
	var config model.GuildVoicePackConfig
	if err := db.First(&config, "guild_id = ?", guildID).Error; err != nil || !config.Enabled || config.AudioURL == "" {
		return false
	}
	var channelConfig model.ChannelVoicePackConfig
	if err := db.First(&channelConfig, "channel_id = ?", channelID).Error; err == nil && !channelConfig.Allowed {
		return false
	}
	event := eventbus.Event{
		Type:    eventbus.EventVoicePackPlay,
		GuildID: &guildID,
		Payload: VoicePackPlayPayload{
			GuildID: guildID, ChannelID: channelID, UserID: userID,
			AudioURL: config.AudioURL, Scope: config.Scope,
		},
	}
	if config.Scope == model.VoicePackSameChannel {
		event.ChannelID = &channelID
	}
	bus.Publish(event)
	return true
}
