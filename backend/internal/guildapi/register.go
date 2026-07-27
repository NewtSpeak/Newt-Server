// Package guildapi 服务器结构管理 REST API（角色 / 频道 / 权限覆盖 / 服务器生命周期），
// 从 httpapi 的后台专属实现中抽出为共享 handler，多认证平面复用同一套代码：
//   - 后台（/api/v1，aud=admin）：deps.CurrentUser 返回真实用户，SystemAdmin 经
//     perms.LoadGuild 获得全服可见/全权限短路；
//   - 用户端（/gapi/v1，aud=client）：同样保留 SystemAdmin——系统所有者在桌面端
//     可管理全部服务器并打开管理员视图（docs 04 FR-32）；
//   - 机器人（/bot-api/v1）：RegisterBot 挂载 RBAC 子集，bot token 鉴权，权限
//     与人类成员同一套 MANAGE_ROLES + 层级校验（反应角色 / 验证门 bot 依赖）。
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
	mountRoleRoutes(guilds, h)
	// 频道与权限覆盖。
	guilds.POST("/channels", h.createChannel)
	guilds.PATCH("/channels", h.reorderChannels)
	mountOverwriteRoutes(guilds, h)

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
	mountTopLevelOverwriteRoutes(authed, h)
	return nil
}

// RegisterBot 挂载机器人开放平面（/bot-api/v1）与 Discord Bot API 对齐的服务器结构能力。
// 权限校验与人类成员完全一致（RBAC 位 + 层级）；不另开特权通道。
//
// 覆盖（对齐 Discord Guild / Channel / Permission）：
//   - 服务器：读取 / 修改（MANAGE_GUILD）/ 图标横幅 / banner 列表
//   - 频道：创建 / 修改 / 删除 / 排序 / 读单频道 / 访问密码解锁
//   - 角色：CRUD + 成员角色绑定
//   - 频道权限覆盖与权限投影
//
// 不挂载：删除服务器、转让所有权（Discord 亦仅所有者可达，且 bot 通常不作所有者）。
// deps.Auth / deps.CurrentUser 必须为 bot 语义（由 botapi.RegisterBotAPI 注入）。
func RegisterBot(group *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}
	authed := group.Group("", deps.Auth)
	guilds := authed.Group("/guilds/:guildID")

	// 服务器（Discord: Get/Modify Guild）。
	guilds.GET("", h.getGuild)
	guilds.PATCH("", h.updateGuild)
	guilds.POST("/icon", h.uploadGuildIcon)
	guilds.DELETE("/icon", h.deleteGuildIcon)
	guilds.POST("/banner", h.uploadGuildBanner)
	guilds.DELETE("/banner", h.deleteGuildBanner)
	guilds.GET("/banners", h.listGuildBanners)
	guilds.POST("/banners", h.addGuildBanner)
	guilds.PATCH("/banners", h.reorderGuildBanners)
	guilds.DELETE("/banners/:bannerID", h.removeGuildBanner)

	// 角色 + 成员角色。
	mountRoleRoutes(guilds, h)

	// 频道结构（Discord: Create/Modify/Delete/List Guild Channels）。
	// 列表已由 botapi listChannels 提供；此处补创建/排序与单频道读写。
	guilds.POST("/channels", h.createChannel)
	guilds.PATCH("/channels", h.reorderChannels)
	guilds.GET("/channels/:channelID", h.getChannelByGuild)
	mountOverwriteRoutes(guilds, h)
	guilds.GET("/channels/:channelID/permissions/@me", h.myChannelPermissions)

	// 顶级频道入口（Discord: Channel resource）。
	authed.GET("/channels/:channelID", h.getChannel)
	authed.PATCH("/channels/:channelID", h.updateChannel)
	authed.DELETE("/channels/:channelID", h.deleteChannel)
	authed.POST("/channels/:channelID/unlock", h.unlockChannel)
	authed.GET("/channels/:channelID/unlock-status", h.unlockStatus)
	mountTopLevelOverwriteRoutes(authed, h)
	return nil
}

// mountRoleRoutes 角色 CRUD + 成员角色绑定（后台/用户端/bot 共用）。
func mountRoleRoutes(guilds *gin.RouterGroup, h *api) {
	guilds.GET("/roles", h.listRoles)
	guilds.GET("/roles/:roleID", h.getRole)
	guilds.POST("/roles", h.createRole)
	guilds.PATCH("/roles", h.reorderRoles)
	guilds.PATCH("/roles/:roleID", h.updateRole)
	guilds.DELETE("/roles/:roleID", h.deleteRole)
	guilds.PUT("/members/:memberID/roles/:roleID", h.assignRole)
	guilds.DELETE("/members/:memberID/roles/:roleID", h.removeRole)
}

// mountOverwriteRoutes guild 前缀下的频道权限覆盖。
func mountOverwriteRoutes(guilds *gin.RouterGroup, h *api) {
	guilds.GET("/channels/:channelID/overwrites", h.listOverwrites)
	guilds.PUT("/channels/:channelID/overwrites/:targetID", h.upsertOverwrite)
	guilds.DELETE("/channels/:channelID/overwrites/:targetID", h.deleteOverwrite)
}

// mountTopLevelOverwriteRoutes 顶级 /channels/{cid} 权限覆盖与权限投影。
func mountTopLevelOverwriteRoutes(authed *gin.RouterGroup, h *api) {
	authed.PUT("/channels/:channelID/overwrites/:targetID", h.upsertOverwriteByChannel)
	authed.DELETE("/channels/:channelID/overwrites/:targetID", h.deleteOverwriteByChannel)
	authed.GET("/channels/:channelID/permissions/@me", h.myChannelPermissionsByChannel)
}
