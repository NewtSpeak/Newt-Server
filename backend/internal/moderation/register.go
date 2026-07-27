// Package moderation 成员治理：邀请/加入、退出、昵称、踢出、Ban Member
//（docs 12 AG.4 / AO.3、Owl-Desktop docs 02/08）。
package moderation

import (
	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
)

// Register 挂载成员治理 REST API（后台管理平面 /api/v1）。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	handlers := &api{deps: deps}
	registerGuildRoutes(v1, deps, handlers)

	v1.GET("/invites/:code", deps.Auth, handlers.inviteInfo)
	v1.POST("/invites/:code/join", deps.Auth, handlers.joinByInvite)
	return nil
}

// RegisterClient 把成员治理端点投影到用户端认证平面（/gapi/v1，aud=client）。
// deps.Auth / deps.CurrentUser 必须为用户端语义（CurrentUser 剥离 SystemAdmin 标志，
// 保证 client 平面无系统管理员短路，全部走标准 RBAC + 层级校验）。
// 凭码加入（POST /invites/{code}/join）由 clientapi 自有实现负责，此处不重复挂载。
func RegisterClient(root *gin.RouterGroup, deps appdeps.Deps) error {
	handlers := &api{deps: deps}
	registerGuildRoutes(root, deps, handlers)

	root.GET("/invites/:code", deps.Auth, handlers.inviteInfo)
	return nil
}

// RegisterBot 挂载机器人开放平面（/bot-api/v1）成员治理能力。
// 对齐 Discord：Kick / Ban / Modify Member / Create Invite / Leave Guild。
// 权限校验与人类成员一致（KICK_MEMBERS / BAN_MEMBERS / MANAGE_NICKNAMES 等 + 层级）。
// 不挂载「凭邀请加入」（bot 不通过邀请码进服，由管理员安装）。
func RegisterBot(group *gin.RouterGroup, deps appdeps.Deps) error {
	handlers := &api{deps: deps}
	registerGuildRoutes(group, deps, handlers)
	group.GET("/invites/:code", deps.Auth, handlers.inviteInfo)
	return nil
}

// registerGuildRoutes 多平面共用的 guild 级治理路由。
func registerGuildRoutes(group *gin.RouterGroup, deps appdeps.Deps, handlers *api) {
	guilds := group.Group("/guilds/:guildID", deps.Auth)
	guilds.POST("/invites", handlers.createInvite)
	// members/{memberID}：DELETE 为踢出（@me 时转主动退出），PATCH 为昵称管理。
	guilds.DELETE("/members/:memberID", handlers.kickMember)
	guilds.PATCH("/members/:memberID", handlers.updateNickname)
	// 本人用户名样式来源角色偏好（不改变角色绑定）
	guilds.PATCH("/members/:memberID/name-style", handlers.updateNameStylePreference)
	guilds.PUT("/bans/:userID", handlers.banUser)
	guilds.DELETE("/bans/:userID", handlers.unbanUser)
	guilds.GET("/bans", handlers.listBans)
}
