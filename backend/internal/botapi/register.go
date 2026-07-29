// Package botapi 机器人（Bot）能力：注册/配置、独立 bot token、手动权限赋予、
// 流式消息、卡片消息，以及供各语言 SDK 调用的开放 API（/bot-api）。
//
// 设计：bot 复用 User(IsBot=true)+Member+Role 的既有权限体系，因此不需要为
// bot 重写一套 RBAC；bot 通过独立的长期 token 鉴权（非密码登录）。
//
// 两个挂载平面：
//   - Register（/api/v1，后台管理）：bot 档案 CRUD、令牌签发/吊销、安装到服/卸载；
//     角色绑定（权限赋予）复用既有成员角色端点（bot 即 Member）。
//   - RegisterBotAPI（/bot-api/v1，开放平面）：bot token 鉴权，能力面与 Discord
//     开放平台 Bot 对齐——消息/反应/角色/频道/成员治理/邀请/Restriction/语音/
//     舞台/审计/贴图，全部走同一套 RBAC + 层级校验，不另开特权通道。
package botapi

import (
	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/auditapi"
	"github.com/newtspeak/newt-server/backend/internal/gateway"
	"github.com/newtspeak/newt-server/backend/internal/guildapi"
	"github.com/newtspeak/newt-server/backend/internal/message"
	"github.com/newtspeak/newt-server/backend/internal/moderation"
	"github.com/newtspeak/newt-server/backend/internal/publicinvite"
	"github.com/newtspeak/newt-server/backend/internal/restriction"
	"github.com/newtspeak/newt-server/backend/internal/stage"
	"github.com/newtspeak/newt-server/backend/internal/sticker"
	"github.com/newtspeak/newt-server/backend/internal/voice"
)

// Register 挂载后台管理侧机器人管理 API（/api/v1）。
// 平台级 bot CRUD 需系统管理员登录；服级创建/token/卸载按 ManageBots 裁决。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	svc := ensureService(deps.DB, deps.Bus, deps.Cfg)
	h := &adminHandlers{service: svc, currentUser: deps.CurrentUser}

	authed := v1.Group("", deps.Auth)
	// 平台级机器人（无 home_guild，可跨服安装）
	authed.POST("/bots", h.createBot)
	authed.GET("/bots", h.listBots)
	authed.GET("/bots/:botID", h.getBot)
	authed.PATCH("/bots/:botID", h.updateBot)
	authed.DELETE("/bots/:botID", h.deleteBot)
	authed.POST("/bots/:botID/tokens", h.createToken)
	authed.GET("/bots/:botID/tokens", h.listTokens)
	authed.DELETE("/bots/:botID/tokens/:tokenID", h.revokeToken)
	// 服级机器人：创建即绑定本服 + token + 删除/卸载
	h.mountGuildBotRoutes(authed)
	return nil
}

// RegisterClient 挂载用户端服级机器人管理（/gapi/v1）。
// 服主或持 MANAGE_BOTS 的成员可在本服创建/配置/签发 token/删除独属机器人。
func RegisterClient(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	svc := ensureService(deps.DB, deps.Bus, deps.Cfg)
	h := &adminHandlers{service: svc, currentUser: deps.CurrentUser}
	authed := v1.Group("", deps.Auth)
	h.mountGuildBotRoutes(authed)
	return nil
}

// RegisterBotAPI 挂载 bot 开放 API（/bot-api 前缀，实际版本化为 /bot-api/v1）。
// 能力面与 Discord Bot API 对齐（在 NewtSpeak 已实现的产品模型内）：
//
//	消息 / 反应 / 附件 / 搜索 / 流式卡片
//	服务器结构 / 频道 CRUD / 角色 / 权限覆盖
//	成员踢出·封禁·昵称 / 邀请 / Restriction（Timeout）
//	语音进房·管理静音闭听移动 / 舞台 / 屏幕共享
//	审计日志 / 贴图表情
//
// 每个领域 handler 内部仍按 RBAC 权限位裁决；bot 仅当被赋予对应角色时才可调用。
func RegisterBotAPI(botAPI *gin.RouterGroup, deps appdeps.Deps) error {
	svc := ensureService(deps.DB, deps.Bus, deps.Cfg)
	v1 := botAPI.Group("/v1")

	// bot 认证平面注入：领域模块经 Deps.Auth/CurrentUser 以「bot 即用户」复用。
	botDeps := deps
	botDeps.Auth = svc.requireBotAuth()
	botDeps.CurrentUser = CurrentBotUser

	// 基础资源目录。
	authed := v1.Group("", botDeps.Auth)
	authed.GET("/me", svc.me)
	authed.GET("/guilds", svc.myGuilds)
	authed.GET("/guilds/:guildID/channels", svc.listChannels)
	authed.GET("/guilds/:guildID/members", svc.listMembers)
	authed.GET("/guilds/:guildID/members/:memberID", svc.getMember)
	authed.GET("/guilds/:guildID/permissions/@me", svc.myGuildPermissions)

	// 消息：CRUD / 附件 / 搜索 / 反应 / 打字 + 卡片 + 流式（bot 专属）。
	if err := message.RegisterBot(v1, botDeps); err != nil {
		return err
	}

	// 服务器结构：guild / 频道 / 角色 / 覆盖（Discord Guild+Channel+Permission）。
	if err := guildapi.RegisterBot(v1, botDeps); err != nil {
		return err
	}

	// 成员治理：踢出 / 封禁 / 昵称 / 创建邀请 / 主动退服（Discord Member+Ban+Invite）。
	if err := moderation.RegisterBot(v1, botDeps); err != nil {
		return err
	}

	// 邀请列表与撤销（Discord Get/Delete Guild Invites）。
	if err := publicinvite.RegisterBot(v1, botDeps); err != nil {
		return err
	}

	// Restriction：超时禁言等多维限制（Discord Timeout / Moderate Members）。
	if err := restriction.RegisterBot(v1, botDeps); err != nil {
		return err
	}

	// 审计日志（Discord Get Guild Audit Log）。
	if err := auditapi.RegisterBot(v1, botDeps); err != nil {
		return err
	}

	// 贴图 / 小表情（Discord Guild Expressions 子集 + 用户贴图库）。
	if err := sticker.RegisterBot(v1, botDeps); err != nil {
		return err
	}

	// 语音：自身进房 + 管理踢出/移动/服务器静音闭听。
	voice.RegisterBotPlane(authed, botDeps)
	voice.RegisterBotPublic(v1, botDeps)

	// 舞台 + 屏幕共享。
	if err := stage.RegisterBot(v1, botDeps); err != nil {
		return err
	}

	// Gateway WS：事件与用户端一致（按频道可见性过滤）。
	return gateway.RegisterWithAuthenticator(v1, deps, svc.authenticateGatewayToken)
}
