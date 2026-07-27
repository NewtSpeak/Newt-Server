package sticker

import (
	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
)

// Register 挂载后台平面（/api/v1）贴图 API + 系统管理员治理端点。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	h := newAPI(deps)
	mountUser(v1, h, deps.Auth)
	mountAdmin(v1, h, deps.Auth)
	return nil
}

// RegisterClient 挂载用户端平面（/gapi/v1）贴图 API（无平台 admin）。
func RegisterClient(root *gin.RouterGroup, deps appdeps.Deps) error {
	h := newAPI(deps)
	mountUser(root, h, deps.Auth)
	return nil
}

// RegisterPublic 挂载贴图资产公开访问（/public-assets/stickers/...）。
func RegisterPublic(pub *gin.RouterGroup, deps appdeps.Deps) error {
	h := newAPI(deps)
	pub.GET("/stickers/:name", h.serveAsset)
	return nil
}

// RegisterBot 挂载机器人开放平面贴图能力（对齐 Discord Guild Emoji/Sticker 管理子集）。
// bot 作为 IsBot 用户可：自建/管理包、贴图库、可用集合查询、服 ban（需 MANAGE_EXPRESSIONS 等）。
// 不挂系统管 purge / 全局 ban 端点。
func RegisterBot(group *gin.RouterGroup, deps appdeps.Deps) error {
	h := newAPI(deps)
	mountUser(group, h, deps.Auth)
	return nil
}

func mountUser(group *gin.RouterGroup, h *api, auth gin.HandlerFunc) {
	me := group.Group("/users/@me", auth)

	// 自建包
	me.GET("/sticker-packs", h.listMyPacks)
	me.POST("/sticker-packs", h.createPack)
	me.PATCH("/sticker-packs/:packID", h.patchPack)
	me.DELETE("/sticker-packs/:packID", h.softDeletePack)
	me.POST("/sticker-packs/:packID/restore", h.restorePack)
	// 自定义封面图（独立于包内条目）
	me.PUT("/sticker-packs/:packID/cover", h.uploadPackCover)
	me.DELETE("/sticker-packs/:packID/cover", h.deletePackCover)
	me.POST("/sticker-packs/:packID/items", h.uploadItem)
	me.PATCH("/sticker-packs/:packID/items/:itemID", h.patchItem)
	me.DELETE("/sticker-packs/:packID/items/:itemID", h.deleteItem)
	me.POST("/sticker-packs/:packID/items/copy", h.copyItem)

	// 贴图库
	me.GET("/sticker-library", h.listLibrary)
	me.PUT("/sticker-library/:packID", h.installPack)
	me.DELETE("/sticker-library/:packID", h.uninstallPack)
	// 可用集合（选择器用；?guild_id= 过滤服独属与 ban）
	me.GET("/sticker-available", h.listAvailable)

	// 包预览 / 条目管理（非 @me 前缀）
	authed := group.Group("", auth)
	authed.GET("/sticker-packs/:packID", h.getPack)
	authed.GET("/sticker-items/:itemID", h.getItem)

	// 服 ban
	authed.GET("/guilds/:guildID/sticker-pack-bans", h.listGuildBans)
	authed.PUT("/guilds/:guildID/sticker-pack-bans/:packID", h.banGuildPack)
	authed.DELETE("/guilds/:guildID/sticker-pack-bans/:packID", h.unbanGuildPack)
}

func mountAdmin(group *gin.RouterGroup, h *api, auth gin.HandlerFunc) {
	admin := group.Group("/admin/sticker-packs", auth, h.requireSystemAdmin())
	admin.GET("", h.adminListPacks)
	admin.POST("/:packID/global-ban", h.adminGlobalBan)
	admin.DELETE("/:packID/global-ban", h.adminGlobalUnban)
	admin.DELETE("/:packID", h.adminPurgePack)
	admin.DELETE("/:packID/items/:itemID", h.adminPurgeItem)

	quota := group.Group("/admin/sticker-quotas", auth, h.requireSystemAdmin())
	quota.GET("/:userID", h.adminGetQuota)
	quota.PUT("/:userID", h.adminPutQuota)
}
