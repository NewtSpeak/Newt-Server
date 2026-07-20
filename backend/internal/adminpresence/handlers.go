package adminpresence

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/message"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// ---------------------------------------------------------------------------
// 平台 / 频道音频审计配置
// ---------------------------------------------------------------------------

// getPlatformAudit GET /admin/audit-config：平台级审计默认。
func (h *api) getPlatformAudit(c *gin.Context) {
	if _, ok := h.requireSystemAdmin(c); !ok {
		return
	}
	c.JSON(http.StatusOK, h.loadPlatformAudit())
}

type platformAuditRequest struct {
	RecordDefault *bool `json:"record_default"`
	NotifyDefault *bool `json:"notify_default"`
}

// putPlatformAudit PUT /admin/audit-config：更新平台级审计默认。
func (h *api) putPlatformAudit(c *gin.Context) {
	user, ok := h.requireSystemAdmin(c)
	if !ok {
		return
	}
	var input platformAuditRequest
	if !bind(c, &input) {
		return
	}
	cfg := h.loadPlatformAudit()
	record, notify := cfg.RecordDefault, cfg.NotifyDefault
	if input.RecordDefault != nil {
		record = *input.RecordDefault
	}
	if input.NotifyDefault != nil {
		notify = *input.NotifyDefault
	}
	// 用 map Assign 强制写入零值布尔（结构体上的 false 会被 GORM 视为零值省略、回落 DB 默认）。
	cfg = model.PlatformAuditConfig{ID: 1}
	if err := h.deps.DB.Where(model.PlatformAuditConfig{ID: 1}).
		Assign(map[string]any{"record_default": record, "notify_default": notify, "updated_at": nowUTC()}).
		FirstOrCreate(&cfg).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存平台审计配置失败")
		return
	}
	actorID := user.ID
	audit.Log(h.deps.DB, audit.Entry{
		ActorID: &actorID, ActorType: "system_admin",
		Action: "adminpresence.platform_audit_update", TargetType: "platform", TargetID: "1",
		Detail: map[string]any{"record_default": cfg.RecordDefault, "notify_default": cfg.NotifyDefault},
	})
	c.JSON(http.StatusOK, cfg)
}

// getChannelAudit GET /admin/channels/{cid}/audit-config：频道有效审计裁决 + 是否独立配置。
func (h *api) getChannelAudit(c *gin.Context) {
	if _, ok := h.requireSystemAdmin(c); !ok {
		return
	}
	channelID, ok := parseUUID(c, "channelID")
	if !ok {
		return
	}
	var channel model.Channel
	if err := h.deps.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	var cfg model.ChannelAuditConfig
	hasOverride := h.deps.DB.First(&cfg, "channel_id = ?", channelID).Error == nil
	record, notify := channelAuditEffective(h.deps.DB, channel.GuildID, channelID)
	c.JSON(http.StatusOK, gin.H{
		"channel_id":   channelID,
		"guild_id":     channel.GuildID,
		"has_override": hasOverride,
		"record":       record,
		"notify":       notify,
	})
}

type channelAuditRequest struct {
	// Inherit=true 时删除频道独立配置，回落平台默认。
	Inherit bool  `json:"inherit"`
	Record  *bool `json:"record"`
	Notify  *bool `json:"notify"`
}

// putChannelAudit PUT /admin/channels/{cid}/audit-config：设置/清除频道独立审计配置。
func (h *api) putChannelAudit(c *gin.Context) {
	user, ok := h.requireSystemAdmin(c)
	if !ok {
		return
	}
	channelID, ok := parseUUID(c, "channelID")
	if !ok {
		return
	}
	var channel model.Channel
	if err := h.deps.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	var input channelAuditRequest
	if !bind(c, &input) {
		return
	}
	if input.Inherit {
		if err := h.deps.DB.Where("channel_id = ?", channelID).Delete(&model.ChannelAuditConfig{}).Error; err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "清除频道审计配置失败")
			return
		}
	} else {
		var existing model.ChannelAuditConfig
		record, notify := false, true
		if h.deps.DB.First(&existing, "channel_id = ?", channelID).Error == nil {
			record, notify = existing.Record, existing.Notify
		}
		if input.Record != nil {
			record = *input.Record
		}
		if input.Notify != nil {
			notify = *input.Notify
		}
		cfg := model.ChannelAuditConfig{ChannelID: channelID}
		if err := h.deps.DB.Where(model.ChannelAuditConfig{ChannelID: channelID}).
			Assign(map[string]any{"guild_id": channel.GuildID, "record": record, "notify": notify, "updated_by": user.ID, "updated_at": nowUTC()}).
			FirstOrCreate(&cfg).Error; err != nil {
			fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存频道审计配置失败")
			return
		}
	}
	// 审计状态变化 → 触发该频道所有在房会话重算 caps/token（新 audit claim 于续签生效）。
	h.deps.Bus.Publish(eventbus.Event{
		Type: eventbus.InternalCapsDirty,
		Payload: eventbus.CapsDirtyPayload{
			GuildID: channel.GuildID.String(), ChannelID: channelID.String(), Reason: "audit_config_changed",
		},
	})
	actorID := user.ID
	audit.Log(h.deps.DB, audit.Entry{
		ActorID: &actorID, ActorType: "system_admin", GuildID: &channel.GuildID,
		Action: "adminpresence.channel_audit_update", TargetType: "channel", TargetID: channelID.String(),
		Detail: map[string]any{"inherit": input.Inherit},
	})
	record, notify := channelAuditEffective(h.deps.DB, channel.GuildID, channelID)
	c.JSON(http.StatusOK, gin.H{"channel_id": channelID, "record": record, "notify": notify, "has_override": !input.Inherit})
}

// ---------------------------------------------------------------------------
// 管理员临场发言（文本频道，以系统管理员身份）
// ---------------------------------------------------------------------------

type presenceMessageRequest struct {
	Content string `json:"content" binding:"required,min=1,max=4000"`
}

// postPresenceMessage POST /admin/channels/{cid}/presence/message：
// 系统管理员从后台直接向文本子频道发言（以本人身份，MESSAGE_CREATE 正常下发）。
func (h *api) postPresenceMessage(c *gin.Context) {
	user, ok := h.requireSystemAdmin(c)
	if !ok {
		return
	}
	channelID, ok := parseUUID(c, "channelID")
	if !ok {
		return
	}
	var channel model.Channel
	if err := h.deps.DB.First(&channel, "id = ?", channelID).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "频道不存在")
		return
	}
	if channel.Type != model.ChannelText {
		fail(c, http.StatusBadRequest, "NOT_TEXT_CHANNEL", "只能向文本频道发言")
		return
	}
	var input presenceMessageRequest
	if !bind(c, &input) {
		return
	}
	// 管理员发言前确保其为该服成员（临场需要 author 可被成员列表解析）；非成员则临时补建成员。
	h.ensureMembership(channel.GuildID, user.ID)
	result, err := message.PostAsUser(h.deps.DB, h.deps.Bus, channel.GuildID, channelID, user.ID, strings.TrimSpace(input.Content))
	if err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "发送消息失败")
		return
	}
	actorID := user.ID
	audit.Log(h.deps.DB, audit.Entry{
		ActorID: &actorID, ActorType: "system_admin", GuildID: &channel.GuildID,
		Action: "adminpresence.text_message", TargetType: "channel", TargetID: channelID.String(),
		Detail: map[string]any{"message_id": result.ID},
	})
	c.JSON(http.StatusCreated, result)
}

// ensureMembership 系统管理员临场时若尚非该服成员，补建一条成员记录（幂等）。
func (h *api) ensureMembership(guildID, userID uuid.UUID) {
	var member model.Member
	if h.deps.DB.First(&member, "guild_id = ? AND user_id = ?", guildID, userID).Error == nil {
		return
	}
	_ = h.deps.DB.Create(&model.Member{ID: uuid.New(), GuildID: guildID, UserID: userID}).Error
}

// ---------------------------------------------------------------------------
// 语音隐身开关
// ---------------------------------------------------------------------------

// getStealth GET /admin/voice/stealth?guild_id=：查询本管理员在某服的隐身状态。
func (h *api) getStealth(c *gin.Context) {
	user, ok := h.requireSystemAdmin(c)
	if !ok {
		return
	}
	guildID, err := uuid.Parse(c.Query("guild_id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "guild_id 非法")
		return
	}
	c.JSON(http.StatusOK, gin.H{"guild_id": guildID, "hidden": stealth(h.deps.DB, guildID, user.ID)})
}

type stealthRequest struct {
	GuildID uuid.UUID `json:"guild_id" binding:"required"`
	Hidden  bool      `json:"hidden"`
}

// putStealth PUT /admin/voice/stealth：设置本管理员在某服语音是否隐身。
// 变化后触发该管理员当前语音会话（若在房）重算 caps/token —— 新 hidden claim
// 与广播抑制在续签后完全生效；隐身状态对成员列表/事件立即生效（预测式查询）。
func (h *api) putStealth(c *gin.Context) {
	user, ok := h.requireSystemAdmin(c)
	if !ok {
		return
	}
	var input stealthRequest
	if !bind(c, &input) {
		return
	}
	presence := model.AdminVoicePresence{GuildID: input.GuildID, UserID: user.ID}
	if err := h.deps.DB.Where(model.AdminVoicePresence{GuildID: input.GuildID, UserID: user.ID}).
		Assign(map[string]any{"hidden": input.Hidden, "updated_at": nowUTC()}).
		FirstOrCreate(&presence).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存隐身设置失败")
		return
	}
	// 若管理员当前在该服语音频道内，触发重算（新 token 反映 hidden，广播随之抑制/恢复）。
	var vs model.VoiceState
	if h.deps.DB.First(&vs, "guild_id = ? AND user_id = ? AND channel_id IS NOT NULL", input.GuildID, user.ID).Error == nil && vs.ChannelID != nil {
		h.deps.Bus.Publish(eventbus.Event{
			Type: eventbus.InternalCapsDirty,
			Payload: eventbus.CapsDirtyPayload{
				GuildID: input.GuildID.String(), ChannelID: vs.ChannelID.String(),
				UserID: user.ID.String(), Reason: "admin_stealth_changed",
			},
		})
	}
	h.deps.Bus.Publish(eventbus.Event{
		Type: eventbus.EventAdminPresenceUpdate, UserIDs: []uuid.UUID{user.ID},
		Payload: gin.H{"guild_id": input.GuildID, "hidden": input.Hidden},
	})
	c.JSON(http.StatusOK, gin.H{"guild_id": input.GuildID, "hidden": input.Hidden})
}

// ---------------------------------------------------------------------------
// 审计录音列表 / 下载（系统管理员）
// ---------------------------------------------------------------------------

// listAuditRecords GET /admin/audit-records?guild_id=&channel_id=&user_id=&limit=。
func (h *api) listAuditRecords(c *gin.Context) {
	if _, ok := h.requireSystemAdmin(c); !ok {
		return
	}
	query := h.deps.DB.Model(&model.AudioAuditRecord{})
	for _, f := range []struct{ param, column string }{
		{"guild_id", "guild_id"}, {"channel_id", "channel_id"}, {"user_id", "user_id"},
	} {
		if raw := c.Query(f.param); raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				fail(c, http.StatusBadRequest, "INVALID_REQUEST", f.param+" 非法")
				return
			}
			query = query.Where(f.column+" = ?", id)
		}
	}
	limit := 100
	var records []model.AudioAuditRecord
	if err := query.Order("created_at DESC").Limit(limit).Find(&records).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取审计录音失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"records": records})
}

func parseUUID(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "资源不存在")
		return uuid.Nil, false
	}
	return id, true
}
