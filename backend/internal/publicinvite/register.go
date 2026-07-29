// Package publicinvite 邀请分享与落地页：
//   - 未安装客户端的用户打开分享链接 → 公开落地页（HTML + JSON API）返回服务器信息 +
//     多条公告/注意事项/协议 + 下载引导（无需登录）；
//   - 已安装客户端 → 深链自动打开并自动加入服务器（客户端不内置链接信息，避免重复填写）；
//   - 同一后端下若已有有效账号，则凭邀请免注册直接加入（预览 + 既有 join 端点）。
package publicinvite

import (
	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
)

// RegisterAdmin 挂载后台管理侧邀请落地内容管理 API（/api/v1）：落地页配置、
// 公告/注意事项/协议内容块、邀请列表（含分享链接与深链）、全局门户配置。
func RegisterAdmin(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}

	guilds := v1.Group("/guilds/:guildID", deps.Auth)
	guilds.GET("/invite-landing", h.getLanding)
	guilds.PUT("/invite-landing", h.putLanding)
	guilds.POST("/invite-notices", h.createNotice)
	guilds.PATCH("/invite-notices/:noticeID", h.updateNotice)
	guilds.DELETE("/invite-notices/:noticeID", h.deleteNotice)
	guilds.GET("/invites", h.listInvites)
	guilds.DELETE("/invites/:code", h.deleteInvite)

	admin := v1.Group("/admin", deps.Auth)
	admin.GET("/invite-portal", h.getPortal)
	admin.PUT("/invite-portal", h.putPortal)
	return nil
}

// RegisterPublic 挂载公开落地页 API（/invite-api，无需登录）：解析分享码 →
// 服务器公开信息、公告/协议列表、下载渠道、深链。
func RegisterPublic(pub *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}
	pub.GET("/invites/:code", h.publicInviteInfo)
	pub.GET("/invites/:code/page", h.landingPage)
	return nil
}

// RegisterLanding 注册友好短链 /invite/{code}（服务端渲染 HTML 落地页），
// 供直接分享给未注册用户；由 server.New 在 web fallback 之前挂载。
func RegisterLanding(router *gin.Engine, deps appdeps.Deps) {
	h := &api{deps: deps}
	router.GET("/invite/:code", h.landingPage)
}

// RegisterClient 用户端（/gapi/v1）邀请端点：登录用户预览邀请（服务器信息 +
// 公告/协议 + 是否已加入），确认后走既有 POST /invites/{code}/join 免注册加入；
// 另含邀请管理投影（列表 / 按码撤销，clientPlane 语义：无 SystemAdmin 短路，
// 权限走标准 RBAC：CREATE_INSTANT_INVITE 或 MANAGE_GUILD）。
var RegisterClient = func(authed *gin.RouterGroup, deps appdeps.Deps) {
	h := &api{deps: deps, clientPlane: true}
	authed.GET("/invites/:code/preview", h.previewInvite)
	authed.GET("/guilds/:guildID/invites", h.listInvites)
	authed.DELETE("/invites/:code", h.revokeInviteByCode)
}

// RegisterBot 机器人开放平面邀请管理（对齐 Discord Get/Delete Guild Invites）。
// bot 无 SystemAdmin 短路（clientPlane=true）；创建邀请走 moderation.RegisterBot。
func RegisterBot(group *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps, clientPlane: true}
	authed := group.Group("", deps.Auth)
	authed.GET("/invites/:code/preview", h.previewInvite)
	authed.GET("/guilds/:guildID/invites", h.listInvites)
	authed.DELETE("/invites/:code", h.revokeInviteByCode)
	authed.DELETE("/guilds/:guildID/invites/:code", h.deleteInvite)
	return nil
}
