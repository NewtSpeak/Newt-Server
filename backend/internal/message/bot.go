package message

import (
	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
)

// bot 开放平面（/bot-api/v1）挂载：复用后台/用户端同一套 handler，
// 仅认证平面（bot token）与 URL 前缀不同。

// RegisterBot 把文本频道能力挂到 bot 开放平面。
//
// root 应为 bot 开放平面根组（如 /bot-api/v1）：签名下载端点保持免登录；
// deps.Auth / deps.CurrentUser 必须为 bot 语义（bot token 校验 + bot 用户读取），
// 由 botapi.RegisterBotAPI 注入。
//
// 与用户端相比额外挂载流式消息端点（MESSAGE_STREAM_*，bot 专属能力）；
// 不启动 GC/保留策略任务（由后台实例统一负责）。
func RegisterBot(root *gin.RouterGroup, deps appdeps.Deps) error {
	svc, err := newService(deps, root.BasePath())
	if err != nil {
		return err
	}
	authed := root.Group("", deps.Auth)
	svc.mountUserRoutes(root, authed)
	// 打字指示：bot 生成回复前可提示「正在输入」。
	authed.POST("/channels/:channelID/typing", svc.postTyping)
	// 流式消息三段协议（bot 专属）。
	svc.mountStream(authed)
	// 交互回调（bot 专属，设计文档 2026-07-26）：ack / reply / update_message。
	authed.POST("/interactions/:interactionID/callback", svc.interactionCallback)
	return nil
}
