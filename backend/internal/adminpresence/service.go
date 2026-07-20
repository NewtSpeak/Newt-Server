package adminpresence

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
)

// api 系统管理员临场 / 音频审计 handler 集合。
type api struct {
	deps appdeps.Deps
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return false
	}
	return true
}

// requireSystemAdmin 临场与审计配置均为系统管理员专属能力。
func (h *api) requireSystemAdmin(c *gin.Context) (model.User, bool) {
	user := h.deps.CurrentUser(c)
	if !user.SystemAdmin {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "仅系统管理员可执行该操作")
		return user, false
	}
	return user, true
}

// loadPlatformAudit 读取平台级审计默认（单行，缺省兜底）。
func (h *api) loadPlatformAudit() model.PlatformAuditConfig {
	cfg := model.PlatformAuditConfig{ID: 1, RecordDefault: false, NotifyDefault: true}
	_ = h.deps.DB.First(&cfg, "id = 1").Error
	return cfg
}

// channelAuditEffective 计算某频道最终审计裁决：频道有独立记录时覆盖平台默认。
// 返回 (record, notify)。
func channelAuditEffective(db *gorm.DB, guildID, channelID uuid.UUID) (record, notify bool) {
	platform := model.PlatformAuditConfig{ID: 1, NotifyDefault: true}
	_ = db.First(&platform, "id = 1").Error
	record, notify = platform.RecordDefault, platform.NotifyDefault
	var channel model.ChannelAuditConfig
	if err := db.First(&channel, "channel_id = ?", channelID).Error; err == nil {
		record, notify = channel.Record, channel.Notify
	}
	return record, notify
}

// stealth 查询某管理员在某 guild 是否隐身。
func stealth(db *gorm.DB, guildID, userID uuid.UUID) bool {
	var presence model.AdminVoicePresence
	if err := db.First(&presence, "guild_id = ? AND user_id = ?", guildID, userID).Error; err != nil {
		return false
	}
	return presence.Hidden
}

func nowUTC() time.Time { return time.Now().UTC() }
