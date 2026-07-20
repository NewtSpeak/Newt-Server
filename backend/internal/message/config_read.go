package message

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/model"
)

// getUploadLimit GET /admin/guilds/{gid}/upload-limit：上传上限读回（系统管理员）。
// upload_limit_bytes=0 表示未配置（跟随平台默认），effective_bytes 为实际生效值。
func (s *service) getUploadLimit(c *gin.Context) {
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
	config := s.loadGuildMessageConfig(guild.ID)
	c.JSON(http.StatusOK, gin.H{
		"guild_id":           guild.ID,
		"upload_limit_bytes": config.UploadLimitBytes,
		"effective_bytes":    s.uploadLimitBytes(guild.ID),
		"default_bytes":      int64(defaultUploadLimitBytes),
	})
}
