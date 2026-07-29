package voice

// bot 开放平面（/bot-api/v1）挂载：复用同一 voice.Service 单例。
//
// 机器人语音接入的设计（bot 专项）：
//   - bot 以 IsBot=true 的 User 走与人类完全相同的进房主路径（调度/级联/caps），
//     其音频权限由「安装到服后绑定的角色/频道覆盖」独立决定（无需客户端或注册账号）；
//   - placeOnNode / refresh 为 bot 用户签发的 Media Token 携带 bot=true claim，
//     SFU 据此在 ready 快照与 participant_joined 信令中带 is_bot 独立标记。

import (
	"log"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
)

// RegisterBotPlane 把语音会话端点挂到 bot 开放平面。
// authed 为已带 bot token 认证的路由组；deps.CurrentUser 读取 bot 用户。
//
// 对齐 Discord Voice State / Move Members / Mute Members：
//   - 自身进房/离房/静音闭听/续签 Media Token
//   - 列出频道 voice states
//   - 管理员踢出语音 / 移动成员 / 服务器静音·闭听（handler 内 RBAC + 层级）
func RegisterBotPlane(authed *gin.RouterGroup, deps appdeps.Deps) {
	svc, err := ensureService(deps)
	if err != nil {
		log.Printf("voice: bot 平面路由挂载失败: %v", err)
		return
	}
	group := authed.Group("", injectCurrentUser(deps.CurrentUser))
	// 自身会话（Discord: 机器人自身 Voice State）。
	tryRegister(func() { group.POST("/voice/join", svc.handleJoin) })
	tryRegister(func() { group.POST("/voice/leave", svc.handleLeave) })
	tryRegister(func() { group.POST("/voice/refresh-token", svc.handleRefreshToken) })
	tryRegister(func() { group.PATCH("/voice/state", svc.handleSelfState) })
	tryRegister(func() { group.POST("/voice/rtt", svc.handleRTTReport) })
	tryRegister(func() { group.POST("/voice/ice-failed", svc.handleIceFailed) })
	tryRegister(func() { group.POST("/voice/ice-failure", svc.handleICEFailure) })
	tryRegister(func() { group.POST("/voice/migrations/:migrationID/ack", svc.handleMigrationAck) })
	// 频道/服级语音管理。
	tryRegister(func() { group.GET("/guilds/:guildID/channels/:channelID/voice-states", svc.handleListVoiceStates) })
	tryRegister(func() { group.GET("/guilds/:guildID/voice/nodes", svc.handleListVoiceNodes) })
	tryRegister(func() { group.POST("/guilds/:guildID/voice/disconnect", svc.handleAdminDisconnect) })
	tryRegister(func() { group.POST("/guilds/:guildID/voice/move", svc.handleAdminMove) })
	tryRegister(func() { group.PATCH("/guilds/:guildID/voice/states/:userID", svc.handleServerState) })
}

// RegisterBotPublic 把免认证公开端点（Media Token 验签公钥）挂到 bot 平面根组。
func RegisterBotPublic(root *gin.RouterGroup, deps appdeps.Deps) {
	svc, err := ensureService(deps)
	if err != nil {
		log.Printf("voice: bot 平面公开路由挂载失败: %v", err)
		return
	}
	tryRegister(func() { root.GET("/voice/public-key", svc.handlePublicKey) })
}
