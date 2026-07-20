package message

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"gorm.io/gorm/clause"
)

// 服级消息配置管理：附件上限（系统管，AT.4）与消息保留天数（服管，AW）。

func (s *service) loadGuildMessageConfig(guildID uuid.UUID) model.GuildMessageConfig {
	config := model.GuildMessageConfig{GuildID: guildID}
	s.db.Where("guild_id = ?", guildID).First(&config)
	return config
}

func (s *service) saveGuildMessageConfig(config *model.GuildMessageConfig) error {
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "guild_id"}},
		UpdateAll: true,
	}).Create(config).Error
}

type uploadLimitRequest struct {
	UploadLimitBytes int64 `json:"upload_limit_bytes"`
}

// patchUploadLimit PATCH /admin/guilds/{gid}/upload-limit：
// 仅系统管理员可调（AT.4 / 5B.3b）；0 表示恢复平台默认 25MB。管理端点对非系统管 404 隐藏。
func (s *service) patchUploadLimit(c *gin.Context) {
	user := s.currentUser(c)
	if !user.SystemAdmin {
		notFound(c)
		return
	}
	guildID, ok := parseUUIDParam(c, "guildID")
	if !ok {
		return
	}
	var guild model.Guild
	if err := s.db.First(&guild, "id = ?", guildID).Error; err != nil {
		notFound(c)
		return
	}
	var input uploadLimitRequest
	if !bind(c, &input) {
		return
	}
	if input.UploadLimitBytes < 0 {
		fail(c, http.StatusBadRequest, "INVALID_LIMIT", "upload_limit_bytes 不能为负数")
		return
	}
	config := s.loadGuildMessageConfig(guild.ID)
	config.UploadLimitBytes = input.UploadLimitBytes
	if err := s.saveGuildMessageConfig(&config); err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存上传上限失败")
		return
	}
	audit.Log(s.db, audit.Entry{
		ActorID: &user.ID, ActorType: "system_admin", GuildID: &guild.ID,
		Action: "message.upload_limit", TargetType: "guild", TargetID: guild.ID.String(),
		Detail: map[string]any{"upload_limit_bytes": input.UploadLimitBytes},
	})
	// 实时同步：在线成员的上传前置校验（GET /guilds/{gid}/upload-limit 缓存）立即失效。
	s.publishGuildConfigUpdate(guild.ID, "upload_limit", gin.H{
		"upload_limit_bytes": config.UploadLimitBytes,
		"effective_bytes":    s.uploadLimitBytes(guild.ID),
	})
	c.JSON(http.StatusOK, gin.H{
		"guild_id":           guild.ID,
		"upload_limit_bytes": config.UploadLimitBytes,
		"effective_bytes":    s.uploadLimitBytes(guild.ID),
	})
}

// getRetention GET /guilds/{gid}/message-retention：服管可读。
func (s *service) getRetention(c *gin.Context) {
	ctx, ok := s.guildAccess(c)
	if !ok {
		return
	}
	if !ctx.Has(rbac.ManageGuild) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "缺少管理服务器权限")
		return
	}
	config := s.loadGuildMessageConfig(ctx.Guild.ID)
	c.JSON(http.StatusOK, gin.H{"guild_id": ctx.Guild.ID, "retention_days": config.RetentionDays})
}

type retentionRequest struct {
	RetentionDays int `json:"retention_days"`
}

// patchRetention PATCH /guilds/{gid}/message-retention：服管配置保留天数（AW.1–2），
// 0 表示永久；到期消息由后台任务每小时硬删（含附件/编辑历史/反应与索引）。
func (s *service) patchRetention(c *gin.Context) {
	ctx, ok := s.guildAccess(c)
	if !ok {
		return
	}
	if !ctx.Has(rbac.ManageGuild) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "缺少管理服务器权限")
		return
	}
	var input retentionRequest
	if !bind(c, &input) {
		return
	}
	if input.RetentionDays < 0 {
		fail(c, http.StatusBadRequest, "INVALID_RETENTION", "retention_days 不能为负数")
		return
	}
	config := s.loadGuildMessageConfig(ctx.Guild.ID)
	config.RetentionDays = input.RetentionDays
	if err := s.saveGuildMessageConfig(&config); err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存保留策略失败")
		return
	}
	actor := s.currentUser(c)
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "guild_admin", GuildID: &ctx.Guild.ID,
		Action: "message.retention", TargetType: "guild", TargetID: ctx.Guild.ID.String(),
		Detail: map[string]any{"retention_days": input.RetentionDays},
	})
	s.publishGuildConfigUpdate(ctx.Guild.ID, "message_retention", gin.H{"retention_days": config.RetentionDays})
	c.JSON(http.StatusOK, gin.H{"guild_id": ctx.Guild.ID, "retention_days": config.RetentionDays})
}
