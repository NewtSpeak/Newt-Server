// Package customization 展示层自定义：角色名样式（纯色/线性/多色/径向渐变）、
// 徽章系统（分配永久/有效天数/截止日期）、用户头像（动态/静态）与横幅。
// 后台频道用户信息需完整呈现这些样式。
package customization

import (
	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
)

// Register 挂载后台管理侧自定义 API（/api/v1）：角色样式编辑、徽章定义与分配、
// 管理员本人头像/横幅上传、成员展示聚合。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}

	guilds := v1.Group("/guilds/:guildID", deps.Auth)
	guilds.PUT("/roles/:roleID/style", h.updateRoleStyle)
	guilds.PUT("/roles/:roleID/badge-icon", h.uploadRoleBadgeIcon)
	guilds.DELETE("/roles/:roleID/badge-icon", h.deleteRoleBadgeIcon)
	guilds.PUT("/roles/:roleID/badge-background", h.uploadRoleBadgeBackground)
	guilds.DELETE("/roles/:roleID/badge-background", h.deleteRoleBadgeBackground)
	guilds.GET("/roles/:roleID/feature-bits", h.getRoleFeatureBits)
	guilds.PATCH("/roles/:roleID/feature-bits", h.patchRoleFeatureBits)
	guilds.GET("/badges", h.listBadges)
	guilds.POST("/badges", h.createBadge)
	guilds.PATCH("/badges/:badgeID", h.updateBadge)
	guilds.DELETE("/badges/:badgeID", h.deleteBadge)
	guilds.GET("/badges/:badgeID/grants", h.listBadgeGrants)
	guilds.PUT("/badges/:badgeID/members/:userID", h.grantBadge)
	guilds.DELETE("/badges/:badgeID/members/:userID", h.revokeBadge)
	guilds.GET("/members/display", h.listMembersDisplay)

	me := v1.Group("/users/@me", deps.Auth)
	me.PATCH("/profile", h.patchProfile)
	me.PUT("/avatar", h.uploadAvatar)
	me.PUT("/banner", h.uploadBanner)
	return nil
}

// RegisterPublic 挂载头像/横幅/角色徽章图标公开访问路由（/public-assets，无需登录；
// 文件名含版本号不可变，允许 CDN/浏览器长缓存）。
func RegisterPublic(pub *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}
	pub.GET("/profile/:name", h.serveProfileAsset)
	pub.GET("/role-badges/:name", h.serveRoleBadgeAsset)
	return nil
}

// RegisterClient 用户端（/gapi/v1）自定义端点：本人上传头像/横幅、角色样式/徽章、
// 读取成员展示聚合（频道成员列表按此渲染样式与徽章）。
var RegisterClient = func(authed *gin.RouterGroup, deps appdeps.Deps) {
	h := &api{deps: deps, clientPlane: true}
	authed.PATCH("/users/@me/profile", h.patchProfile)
	authed.PUT("/users/@me/avatar", h.uploadAvatar)
	authed.PUT("/users/@me/banner", h.uploadBanner)
	authed.GET("/guilds/:guildID/members/display", h.listMembersDisplay)
	authed.GET("/guilds/:guildID/badges", h.listBadges)
	// 角色名样式 + 徽章 icon（与管理端同源，权限在 handler 内裁决）
	authed.PUT("/guilds/:guildID/roles/:roleID/style", h.updateRoleStyle)
	authed.PUT("/guilds/:guildID/roles/:roleID/badge-icon", h.uploadRoleBadgeIcon)
	authed.DELETE("/guilds/:guildID/roles/:roleID/badge-icon", h.deleteRoleBadgeIcon)
	authed.PUT("/guilds/:guildID/roles/:roleID/badge-background", h.uploadRoleBadgeBackground)
	authed.DELETE("/guilds/:guildID/roles/:roleID/badge-background", h.deleteRoleBadgeBackground)
}
