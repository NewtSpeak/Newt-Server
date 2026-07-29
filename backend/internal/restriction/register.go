package restriction

import (
	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
)

// Register 注入真实 Service 实现、挂载 Restriction REST API 并启动过期扫描（docs 12）。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	svc := newService(deps.DB, deps.Bus)
	impl = svc
	SetService(svc)
	go svc.expiryLoop()

	mountRoutes(v1, deps, &api{deps: deps, svc: svc})
	return nil
}

// RegisterClient 把 Restriction REST API 投影到用户端认证平面（/gapi/v1，aud=client）。
// 复用 Register 构造的同一 service 单例（过期扫描/缓存只此一份）；后台 Register
// 未先行完成时（纯用户端单测装配）就地构造，语义与 Register 一致。
// deps.CurrentUser 必须为剥离 SystemAdmin 标志的用户端读取函数（clientapi 注入），
// 保证 client 平面无系统管理员短路，权限走 MODERATE_MEMBERS/协管路径。
func RegisterClient(root *gin.RouterGroup, deps appdeps.Deps) error {
	svc := impl
	if svc == nil {
		svc = newService(deps.DB, deps.Bus)
		impl = svc
		SetService(svc)
		go svc.expiryLoop()
	}
	mountRoutes(root, deps, &api{deps: deps, svc: svc})
	return nil
}

// RegisterBot 挂载机器人开放平面 Restriction API（对齐 Discord Timeout / Moderate Members）。
// 权限：MODERATE_MEMBERS + 层级；与用户端/后台同一 service 单例。
func RegisterBot(group *gin.RouterGroup, deps appdeps.Deps) error {
	svc := impl
	if svc == nil {
		svc = newService(deps.DB, deps.Bus)
		impl = svc
		SetService(svc)
		go svc.expiryLoop()
	}
	mountRoutes(group, deps, &api{deps: deps, svc: svc})
	return nil
}

func mountRoutes(group *gin.RouterGroup, deps appdeps.Deps, handlers *api) {
	routes := group.Group("/guilds/:guildID/restrictions", deps.Auth)
	routes.POST("", handlers.create)
	routes.GET("", handlers.list)
	// @me 与 {id} 共用 :restrictionID 段，handler 内区分，避免路由树冲突。
	routes.GET("/:restrictionID", handlers.detail)
	routes.PATCH("/:restrictionID", handlers.patch)
	routes.DELETE("/:restrictionID", handlers.lift)
}
