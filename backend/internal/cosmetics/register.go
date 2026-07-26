package cosmetics

import (
	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
)

// Register 后台平面：管理 CRUD + 用户端同源读接口（admin token）。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	if err := SeedCategories(deps.DB); err != nil {
		return err
	}
	h := newAPI(deps)
	mountAdmin(v1, h, deps.Auth)
	// 管理后台也可浏览商店目录（调试）
	mountUser(v1, h, deps.Auth)
	return nil
}

// RegisterClient 用户端平面 /gapi/v1。
func RegisterClient(root *gin.RouterGroup, deps appdeps.Deps) error {
	if err := SeedCategories(deps.DB); err != nil {
		return err
	}
	h := newAPI(deps)
	mountUser(root, h, deps.Auth)
	return nil
}

// RegisterPublic 公开资产 /public-assets/cosmetics/:name
func RegisterPublic(pub *gin.RouterGroup, deps appdeps.Deps) error {
	h := newAPI(deps)
	pub.GET("/cosmetics/:name", h.serveAsset)
	return nil
}

func mountUser(group *gin.RouterGroup, h *api, auth gin.HandlerFunc) {
	authed := group.Group("", auth)

	authed.GET("/cosmetics/categories", h.listCategories)
	authed.GET("/cosmetics/tags", h.listTags)
	authed.GET("/cosmetics/shop", h.listShop)
	authed.GET("/cosmetics/items/:itemID", h.getItem)
	authed.GET("/cosmetics/bundles/:bundleID", h.getBundle)

	me := group.Group("/users/@me", auth)
	me.GET("/cosmetics/inventory", h.listInventory)
	me.GET("/cosmetics/loadout", h.listLoadout)
	me.PUT("/cosmetics/loadout/:slot", h.equipSlot)
	me.DELETE("/cosmetics/loadout/:slot", h.unequipSlot)
	me.POST("/cosmetics/claim", h.claimFree)
	me.POST("/cosmetics/purchase", h.purchaseWithPoints)
	me.GET("/cosmetics/points", h.getMyPoints)
	me.GET("/cosmetics/points/ledger", h.listMyLedger)
	me.POST("/cosmetics/points/exchange", h.exchangeCurrencyStub)

	// 参数名必须与 userapi 的 /users/:id 一致，否则 gin 路由树冲突 panic
	authed.GET("/users/:id/cosmetics/equipped", h.getUserEquipped)
}

func mountAdmin(group *gin.RouterGroup, h *api, auth gin.HandlerFunc) {
	admin := group.Group("/admin/cosmetics", auth, h.requireSystemAdmin())
	admin.Use(func(c *gin.Context) {
		c.Set("cosmetics_admin", true)
		c.Next()
	})

	admin.GET("/categories", h.listCategories)
	admin.POST("/categories", h.createCategory)
	admin.PATCH("/categories/:key", h.patchCategory)

	admin.GET("/tags", h.listTags)
	admin.POST("/tags", h.createTag)
	admin.PATCH("/tags/:tagID", h.patchTag)
	admin.DELETE("/tags/:tagID", h.deleteTag)

	admin.GET("/items", h.adminListItems)
	admin.POST("/items", h.createItem)
	admin.PATCH("/items/:itemID", h.patchItem)
	admin.PUT("/items/:itemID/assets/:slot", h.uploadItemAsset)
	admin.GET("/items/:itemID", h.getItem)

	admin.GET("/bundles", h.adminListBundles)
	admin.POST("/bundles", h.createBundle)
	admin.PATCH("/bundles/:bundleID", h.patchBundle)
	admin.GET("/bundles/:bundleID", h.getBundle)

	admin.POST("/grant", h.adminGrant)
	admin.POST("/points/grant", h.adminGrantPoints)

	admin.GET("/avatar-frames", h.adminAvatarFrames)
}
