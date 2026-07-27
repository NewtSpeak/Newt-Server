package stage

// bot 开放平面（/bot-api/v1）挂载：复用同一 stage service 单例。
// 对齐 Discord Stage Instance / Stage Speaker 管理能力。

import (
	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
)

// RegisterBot 把舞台与屏幕共享端点挂到 bot 开放平面。
// 与 RegisterClient 同构：权限由 handler 内 RBAC 校验（STAGE_* / STREAM / STREAM_END_OTHERS）。
// 不挂系统管配额治理端点。
func RegisterBot(group *gin.RouterGroup, deps appdeps.Deps) error {
	svc := ensureService(deps)
	h := &handlers{svc: svc, currentUser: deps.CurrentUser}

	authed := group.Group("", deps.Auth)
	channels := authed.Group("/channels/:channelID")
	channels.GET("/voice-stage", h.getVoiceStage)
	channels.PATCH("/voice-stage", h.patchVoiceStage)
	channels.GET("/stage/queue", h.getQueue)
	channels.DELETE("/stage/queue/:userID", h.removeFromQueue)
	channels.POST("/stage/apply", h.apply)
	channels.DELETE("/stage/apply", h.cancelApply)
	channels.POST("/stage/bring-up", h.bringUp)
	channels.POST("/stage/bring-down", h.bringDown)
	channels.POST("/stage/self-leave", h.selfLeave)
	channels.POST("/voice/screen/start", h.screenStart)
	channels.POST("/voice/screen/stop", h.screenStop)
	channels.POST("/voice/screen/stop-user", h.screenStopUser)
	authed.GET("/guilds/:guildID/screen-quota", h.guildScreenQuota)
	return nil
}
