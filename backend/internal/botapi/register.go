// Package botapi 机器人（Bot）能力：注册/配置、独立 bot token、手动权限赋予、
// 流式消息、卡片消息，以及供各语言 SDK 调用的开放 API（/bot-api）。
//
// 设计：bot 复用 User(IsBot=true)+Member+Role 的既有权限体系，因此不需要为
// bot 重写一套 RBAC；bot 通过独立的长期 token 鉴权（非密码登录）。
//
// 两个挂载平面：
//   - Register（/api/v1，后台管理）：bot 档案 CRUD、令牌签发/吊销、安装到服/卸载；
//     角色绑定（权限赋予）复用既有成员角色端点（bot 即 Member）。
//   - RegisterBotAPI（/bot-api/v1，开放平面）：bot token 鉴权，完整的消息
//     （含卡片 + 流式三段协议）、语音进出房（Media Token 带 bot 标记）、
//     Gateway WS 事件订阅与基础资源目录，供各语言 SDK 调用。
package botapi

import (
	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/gateway"
	"github.com/owlspeak/owl-server/backend/internal/message"
	"github.com/owlspeak/owl-server/backend/internal/voice"
)

// Register 挂载后台管理侧机器人管理 API（/api/v1）。
// 后台平面仅系统管理员可登录；服级安装/卸载仍按 ManageBots 权限位裁决。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	svc := ensureService(deps.DB, deps.Bus, deps.Cfg)
	h := &adminHandlers{service: svc, currentUser: deps.CurrentUser}

	authed := v1.Group("", deps.Auth)
	authed.POST("/bots", h.createBot)
	authed.GET("/bots", h.listBots)
	authed.GET("/bots/:botID", h.getBot)
	authed.PATCH("/bots/:botID", h.updateBot)
	authed.DELETE("/bots/:botID", h.deleteBot)
	authed.POST("/bots/:botID/tokens", h.createToken)
	authed.GET("/bots/:botID/tokens", h.listTokens)
	authed.DELETE("/bots/:botID/tokens/:tokenID", h.revokeToken)
	authed.GET("/guilds/:guildID/bots", h.listGuildBots)
	authed.PUT("/guilds/:guildID/bots/:botID", h.installBot)
	authed.DELETE("/guilds/:guildID/bots/:botID", h.uninstallBot)
	return nil
}

// RegisterBotAPI 挂载 bot 开放 API（/bot-api 前缀，实际版本化为 /bot-api/v1）。
func RegisterBotAPI(botAPI *gin.RouterGroup, deps appdeps.Deps) error {
	svc := ensureService(deps.DB, deps.Bus, deps.Cfg)
	v1 := botAPI.Group("/v1")

	// bot 认证平面注入：领域模块（message/voice）经 Deps.Auth/CurrentUser
	// 以「bot 即用户」复用全部权限计算与业务逻辑。
	botDeps := deps
	botDeps.Auth = svc.requireBotAuth()
	botDeps.CurrentUser = CurrentBotUser

	// 基础资源目录。
	authed := v1.Group("", botDeps.Auth)
	authed.GET("/me", svc.me)
	authed.GET("/guilds", svc.myGuilds)
	authed.GET("/guilds/:guildID/channels", svc.listChannels)
	authed.GET("/guilds/:guildID/members", svc.listMembers)
	authed.GET("/guilds/:guildID/permissions/@me", svc.myGuildPermissions)

	// 消息：完整用户级能力（CRUD/附件/搜索/反应/打字）+ 卡片 + 流式三段协议。
	if err := message.RegisterBot(v1, botDeps); err != nil {
		return err
	}

	// 语音：进出房/续签/自我状态/成员列表；签发的 Media Token 携带 bot=true，
	// SFU 在音频流参与者信令中给 bot 独立标记。
	voice.RegisterBotPlane(authed, botDeps)
	voice.RegisterBotPublic(v1, botDeps)

	// Gateway WS：IDENTIFY 直接携带 bot token（无需 JWT），事件推送按
	// guild 成员关系 + 频道可见性过滤，与其他平面完全一致。
	return gateway.RegisterWithAuthenticator(v1, deps, svc.authenticateGatewayToken)
}
