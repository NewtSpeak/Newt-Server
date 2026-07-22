// Package guildapi 服务器结构管理 REST API（角色 / 频道 / 权限覆盖 / 服务器生命周期），
// 从 httpapi 的后台专属实现中抽出为共享 handler，双认证平面复用同一套代码：
//   - 后台（/api/v1，aud=admin）：deps.CurrentUser 返回真实用户，SystemAdmin 经
//     perms.LoadGuild 获得全服可见/全权限短路；
//   - 用户端（/gapi/v1，aud=client）：同样保留 SystemAdmin——系统所有者在桌面端
//     可管理全部服务器并打开管理员视图（docs 04 FR-32）。
//
// 错误语义遵循仓库约定：不可见即不存在（无 VIEW_CHANNEL / 非成员一律 404，
// docs 06 议题 8 防扫频）；有可见性但权限不足返回 403。
package guildapi

import (
	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
)

// Register 挂载服务器结构管理路由。group 为未认证的平面根组（/api/v1 或 /gapi/v1），
// 认证中间件取 deps.Auth（各平面注入自己的实现）。
func Register(group *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}
	authed := group.Group("", deps.Auth)

	guilds := authed.Group("/guilds/:guildID")
	// 服务器生命周期。
	guilds.GET("", h.getGuild)
	guilds.PATCH("", h.updateGuild)
	guilds.DELETE("", h.deleteGuild)
	guilds.POST("/transfer-ownership", h.transferOwnership)
	// 服务器图标 / 横幅（Owl-Desktop docs 02 FR-13/§8-9）。
	guilds.POST("/icon", h.uploadGuildIcon)
	guilds.DELETE("/icon", h.deleteGuildIcon)
	guilds.POST("/banner", h.uploadGuildBanner)
	guilds.DELETE("/banner", h.deleteGuildBanner)
	// 服务器多 banner 列表（服务器外观专项）：成员可读，增删/排序需 MANAGE_GUILD。
	guilds.GET("/banners", h.listGuildBanners)
	guilds.POST("/banners", h.addGuildBanner)
	guilds.PATCH("/banners", h.reorderGuildBanners)
	guilds.DELETE("/banners/:bannerID", h.removeGuildBanner)

	// 角色。
	guilds.GET("/roles", h.listRoles)
	guilds.POST("/roles", h.createRole)
	guilds.PATCH("/roles", h.reorderRoles)
	guilds.PATCH("/roles/:roleID", h.updateRole)
	guilds.DELETE("/roles/:roleID", h.deleteRole)
	guilds.PUT("/members/:memberID/roles/:roleID", h.assignRole)
	guilds.DELETE("/members/:memberID/roles/:roleID", h.removeRole)

	// 频道与权限覆盖。
	guilds.POST("/channels", h.createChannel)
	guilds.PATCH("/channels", h.reorderChannels)
	guilds.GET("/channels/:channelID/overwrites", h.listOverwrites)
	guilds.PUT("/channels/:channelID/overwrites/:targetID", h.upsertOverwrite)
	guilds.DELETE("/channels/:channelID/overwrites/:targetID", h.deleteOverwrite)

	// 权限投影查询（客户端灰置 UI 用）。
	guilds.GET("/permissions/@me", h.myGuildPermissions)
	guilds.GET("/channels/:channelID/permissions/@me", h.myChannelPermissions)

	// 顶级频道资源（PATCH/DELETE /channels/{cid}，Owl-Desktop docs 03 FR-09/FR-11）。
	authed.PATCH("/channels/:channelID", h.updateChannel)
	authed.DELETE("/channels/:channelID", h.deleteChannel)
	// 频道访问密码解锁（上锁频道访问消息/语音前调用）。
	authed.POST("/channels/:channelID/unlock", h.unlockChannel)
	authed.GET("/channels/:channelID/unlock-status", h.unlockStatus)
	// 顶级频道权限覆盖与权限投影（Owl-Desktop docs 04 FR-15：客户端以频道为入口，
	// 不强制携带 guildID）。语义与 guild 前缀版一致，额外要求调用者对频道可见
	//（无 VIEW_CHANNEL 一律 404，防扫频）。
	authed.PUT("/channels/:channelID/overwrites/:targetID", h.upsertOverwriteByChannel)
	authed.DELETE("/channels/:channelID/overwrites/:targetID", h.deleteOverwriteByChannel)
	authed.GET("/channels/:channelID/permissions/@me", h.myChannelPermissionsByChannel)
	return nil
}
