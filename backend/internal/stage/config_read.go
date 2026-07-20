package stage

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// getVoiceStage GET /channels/{cid}/voice-stage：舞台配置读回（控制台/客户端编辑前回显）。
// 可见该频道即可读（写入权限在 PATCH 侧裁决）。
func (h *handlers) getVoiceStage(c *gin.Context) {
	scope, ok := h.voiceChannelScope(c)
	if !ok {
		return
	}
	db := h.svc.db
	cfg := h.svc.channelConfig(db, scope.channel.GuildID, scope.channel.ID)

	var coMods []model.StageCoModerator
	db.Where("channel_id = ?", scope.channel.ID).Find(&coMods)
	coModIDs := make([]uuid.UUID, 0, len(coMods))
	for _, mod := range coMods {
		coModIDs = append(coModIDs, mod.UserID)
	}

	// 频道屏幕并发上限：无记录时返回 -1 表示「跟随默认」。
	maxScreens := -1
	var quota model.ScreenChannelQuota
	if err := db.First(&quota, "channel_id = ?", scope.channel.ID).Error; err == nil {
		maxScreens = quota.MaxConcurrentScreens
	}

	c.JSON(http.StatusOK, gin.H{
		"channel_id":               scope.channel.ID,
		"mode":                     cfg.Mode,
		"max_speakers":             cfg.MaxSpeakers,
		"request_to_speak_enabled": cfg.RequestToSpeakEnabled,
		"allow_co_mod_change_mode": cfg.AllowCoModChangeMode,
		"co_moderator_ids":         coModIDs,
		"max_concurrent_screens":   maxScreens,
	})
}
