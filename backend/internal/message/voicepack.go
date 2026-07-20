package message

import (
	"net/http"
	"sync"
	"time"

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
	// 实时同步：成员端即时得知语音包开关/触发模式/范围变化。
	s.publishGuildConfigUpdate(ctx.Guild.ID, "voice_pack", config)
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
	// Allowed 列带 default:true：GORM struct Create 会跳过零值 false 导致「关不掉」，
	// 故先 DoNothing upsert 保证行存在，再用显式 Update 强制写入布尔值。
	config := model.ChannelVoicePackConfig{ChannelID: channel.ID, GuildID: ctx.Guild.ID, Allowed: input.Allowed}
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}},
		DoNothing: true,
	}).Create(&config).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存频道语音包开关失败")
		return
	}
	if err := s.db.Model(&model.ChannelVoicePackConfig{}).Where("channel_id = ?", channel.ID).
		Update("allowed", input.Allowed).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存频道语音包开关失败")
		return
	}
	config.Allowed = input.Allowed
	actor := s.currentUser(c)
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, GuildID: &ctx.Guild.ID,
		Action: "voicepack.channel_toggle", TargetType: "channel", TargetID: channel.ID.String(),
		Detail: map[string]any{"allowed": input.Allowed},
	})
	// 实时同步：带 ChannelID 按可见性过滤下发（频道级配置不外泄）。
	s.publishChannelConfigUpdate(ctx.Guild.ID, channel.ID, "voice_pack", config)
	c.JSON(http.StatusOK, config)
}

// VoicePackScene 触发场景（docs 12 FR-01 / 5A.1）：进服首次出现 / 进指定语音频道。
type VoicePackScene string

const (
	VoicePackSceneFirstJoin   VoicePackScene = "FIRST_JOIN"
	VoicePackSceneChannelJoin VoicePackScene = "CHANNEL_JOIN"
)

// VoicePackPlayPayload EventVoicePackPlay 的载荷（docs 12 §6.1 schema 定稿）：
// 客户端收到后拉取 audio_url 本地混音播放（5A.3，不经 SFU）。
// PackID 为用户选中的语音包 ID；回退服级默认 audio_url 时为空。
type VoicePackPlayPayload struct {
	GuildID   uuid.UUID            `json:"guild_id"`
	ChannelID uuid.UUID            `json:"channel_id"`
	UserID    uuid.UUID            `json:"user_id"`
	PackID    *uuid.UUID           `json:"pack_id,omitempty"`
	AudioURL  string               `json:"audio_url"`
	Scene     VoicePackScene       `json:"scene"`
	Scope     model.VoicePackScope `json:"scope"`
	EventAt   time.Time            `json:"event_at"`
}

// voicePackCooldownWindow 服务端频控窗口（docs 12 US-7 双层频控的服务端层）：
// 同一用户在同一 guild 60s 内至多触发一次，冷却内不发事件。
// 与客户端 60s 本地冷却（FR-17）对齐。
const voicePackCooldownWindow = 60 * time.Second

// voicePackCooldown 内存频控表：guild|user → 最近触发时刻。惰性清理过期项。
type voicePackCooldown struct {
	mu   sync.Mutex
	last map[[2]uuid.UUID]time.Time
}

// allow 冷却外返回 true 并记账；冷却内返回 false。
func (c *voicePackCooldown) allow(guildID, userID uuid.UUID, now time.Time) bool {
	key := [2]uuid.UUID{guildID, userID}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		c.last = map[[2]uuid.UUID]time.Time{}
	}
	if at, ok := c.last[key]; ok && now.Sub(at) < voicePackCooldownWindow {
		return false
	}
	for k, at := range c.last {
		if now.Sub(at) >= voicePackCooldownWindow {
			delete(c.last, k)
		}
	}
	c.last[key] = now
	return true
}

// playCooldown 进程级频控单例（服务端权威频控，客户端频控为防御性叠加）。
var playCooldown = &voicePackCooldown{}

// resolveVoicePackAudio 决定本次播放的音频（docs 12 FR-12 越权回退）：
//  1. 用户在该服选中的包：包仍存在、启用且授权仍有效（RARE 失去身份组即失效）→ 用选中包；
//  2. 否则回退服级默认 audio_url（GuildVoicePackConfig.AudioURL）；两者皆空 → 不播。
func resolveVoicePackAudio(db *gorm.DB, config model.GuildVoicePackConfig, guildID, userID uuid.UUID) (packID *uuid.UUID, audioURL string) {
	var selection model.VoicePackSelection
	if err := db.First(&selection, "guild_id = ? AND user_id = ?", guildID, userID).Error; err == nil {
		var pack model.VoicePack
		if err := db.First(&pack, "id = ? AND guild_id = ?", selection.PackID, guildID).Error; err == nil &&
			pack.Enabled && pack.AudioURL != "" && packAuthorized(db, pack, userID) {
			return &pack.ID, pack.AudioURL
		}
	}
	return nil, config.AudioURL
}

// PlayVoicePack 供语音模块在用户进入语音频道时调用；本函数完成全部触发裁决并发布
// EventVoicePackPlay，返回是否实际发布。firstEverJoin 表示该用户在该服从未连过麦
//（VoiceState 行不存在，由调用方在进房前判定）。
//
// 裁决顺序（docs 12 §5.1）：
//  1. 服级开关（GuildVoicePackConfig.Enabled）；
//  2. 触发场景（5A.1）：FIRST_GUILD_JOIN 仅进服首次；CHANNEL_JOIN 每次进入允许播放的
//     语音频道都触发（受服务端频控约束，无「每次切频都放」的无频控形态）；
//  3. 频道级开关（5A.1b，无记录默认允许）；
//  4. 服务端频控：同一用户同一 guild 60s 冷却；
//  5. 音频裁决：优先用户选中的包（授权仍有效），回退服级默认 audio_url，皆空不播。
//
// 事件范围（5A.2）：两种 Scope 的事件都带 ChannelID——Gateway 按 VIEW_CHANNEL
// 可见性过滤，防止对隐藏频道无权限的成员经载荷 channel_id/user_id 得知
//「谁进入了哪个隐藏频道」。SAME_CHANNEL 与 GUILD_ONLINE 的差别由客户端按
// 载荷 scope 字段裁决（同频道在房 vs 全服可听），服务端受众上界一致为
//「对该频道可见的在线成员」。
func PlayVoicePack(db *gorm.DB, bus *eventbus.Bus, guildID, channelID, userID uuid.UUID, firstEverJoin bool) bool {
	var config model.GuildVoicePackConfig
	if err := db.First(&config, "guild_id = ?", guildID).Error; err != nil || !config.Enabled {
		return false
	}
	var scene VoicePackScene
	switch config.Trigger {
	case model.VoicePackChannelJoin:
		scene = VoicePackSceneChannelJoin
	default: // FIRST_GUILD_JOIN（默认）：仅进服首次出现触发（5A.1）。
		if !firstEverJoin {
			return false
		}
		scene = VoicePackSceneFirstJoin
	}
	var channelConfig model.ChannelVoicePackConfig
	if err := db.First(&channelConfig, "channel_id = ?", channelID).Error; err == nil && !channelConfig.Allowed {
		return false
	}
	now := time.Now().UTC()
	if !playCooldown.allow(guildID, userID, now) {
		return false
	}
	packID, audioURL := resolveVoicePackAudio(db, config, guildID, userID)
	if audioURL == "" {
		return false
	}
	event := eventbus.Event{
		Type:      eventbus.EventVoicePackPlay,
		GuildID:   &guildID,
		ChannelID: &channelID, // 恒带频道 ID：hub 按 VIEW_CHANNEL 过滤，堵住隐藏频道泄露
		Payload: VoicePackPlayPayload{
			GuildID: guildID, ChannelID: channelID, UserID: userID,
			PackID: packID, AudioURL: audioURL, Scene: scene,
			Scope: config.Scope, EventAt: now,
		},
	}
	bus.Publish(event)
	return true
}
