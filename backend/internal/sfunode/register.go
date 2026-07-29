// Package sfunode SFU 节点生命周期管理：REST enrollment（历史路径）、内置文件 CA、
// 节点池管理 REST API（docs 03、07 专项 2）。
//
// 收敛说明（语音端到端统一专项）：本包早期自研的 WSS 控制通道（control.go）与
// sfuctl.Directory / Controller 实现（directory.go / controller.go）与真实 Newt-SFU
// 的 gRPC 控制面协议（proto/owlsfu/v1）不兼容，已停止监听与注入；
// sfuctl 的真实实现改由 internal/sfubridge 提供（server.New 装配）。
// 保留内容：节点池 REST API（/admin/guilds/:gid/node-pool、/guilds/:gid/node-pool）、
// 节点生命周期动作（drain/undrain/disable/enable，指令经 sfuctl 桥接下发）、
// REST enrollment 端点（真实 Newt-SFU 使用 sfucontrol 的 gRPC Enroll，此端点仅兼容保留）。
package sfunode

import (
	"log"

	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
)

// Register 挂载节点管理/enrollment/节点池 REST API。
// 注意：不再启动 WSS 控制通道监听，也不再注入 sfuctl 实现（见包注释）。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	ca, err := LoadOrCreateCA(deps.Cfg.DataDir)
	if err != nil {
		return err
	}
	svc := NewService(deps.DB, deps.Bus, ca)
	hub := NewHub(svc)
	svc.hub = hub

	handlers := &api{svc: svc, hub: hub, deps: deps, controlAddress: deps.Cfg.ControlAddress}

	// 节点侧 enrollment：无需登录，凭一次性 token（docs 03 §4.1）。
	mount("POST /sfu/enroll", func() { v1.POST("/sfu/enroll", handlers.enroll) })

	// 系统管理员：节点生命周期与平台级节点池授权。
	admin := v1.Group("/admin", deps.Auth, handlers.requireSystemAdmin())
	mount("POST /admin/sfu/nodes", func() { admin.POST("/sfu/nodes", handlers.createNode) })
	mount("GET /admin/sfu/nodes", func() { admin.GET("/sfu/nodes", handlers.listNodes) })
	// 拓扑必须在 nodes/:nodeID 之前注册（避免被参数路由误匹配；且与 httpapi 双挂时 mount 幂等跳过）。
	mount("GET /admin/sfu/topology", func() { admin.GET("/sfu/topology", handlers.listTopology) })
	mount("GET /admin/sfu/nodes/:nodeID", func() { admin.GET("/sfu/nodes/:nodeID", handlers.getNode) })
	mount("PATCH /admin/sfu/nodes/:nodeID", func() { admin.PATCH("/sfu/nodes/:nodeID", handlers.updateNode) })
	mount("POST /admin/sfu/nodes/:nodeID/revoke", func() { admin.POST("/sfu/nodes/:nodeID/revoke", handlers.nodeAction(svc.Revoke)) })
	mount("POST /admin/sfu/nodes/:nodeID/drain", func() { admin.POST("/sfu/nodes/:nodeID/drain", handlers.nodeAction(svc.Drain)) })
	mount("POST /admin/sfu/nodes/:nodeID/undrain", func() { admin.POST("/sfu/nodes/:nodeID/undrain", handlers.nodeAction(svc.Undrain)) })
	// disable = 仅关闭调度开关（保持 ONLINE/ENROLLED 状态）；生命周期禁用见 PATCH status=DISABLED。
	mount("POST /admin/sfu/nodes/:nodeID/disable", func() { admin.POST("/sfu/nodes/:nodeID/disable", handlers.nodeAction(svc.DisableScheduling)) })
	// enable = 打开调度开关；DISABLED 时顺带解禁为 ENROLLED（在线则回 ONLINE）。
	mount("POST /admin/sfu/nodes/:nodeID/enable", func() { admin.POST("/sfu/nodes/:nodeID/enable", handlers.nodeAction(svc.Enable)) })
	mount("GET /admin/guilds/:guildID/node-pool", func() { admin.GET("/guilds/:guildID/node-pool", handlers.adminGetPool) })
	mount("PUT /admin/guilds/:guildID/node-pool", func() { admin.PUT("/guilds/:guildID/node-pool", handlers.adminPutPool) })

	// 服务器管理员：从系统管授权候选集中勾选本服节点池。
	guild := v1.Group("/guilds/:guildID", deps.Auth)
	mount("GET /guilds/:guildID/node-pool", func() { guild.GET("/node-pool", handlers.guildGetPool) })
	mount("PUT /guilds/:guildID/node-pool", func() { guild.PUT("/node-pool", handlers.guildPutPool) })

	// 供用户端平面（RegisterClient）复用同一 Service/Hub 实例。
	sharedAPI = handlers
	return nil
}

// sharedAPI Register 构造的 handler 集合（Service + Hub 全进程唯一）。
var sharedAPI *api

// RegisterClient 把服级节点池端点投影到用户端认证平面（/gapi/v1，aud=client）：
//   - GET/PUT /guilds/{gid}/node-pool（需 MANAGE_GUILD，无权限一律 404）
//
// 候选集授权仍仅系统管理员在后台（/admin/guilds/{gid}/node-pool）可改；
// deps.CurrentUser 必须为剥离 SystemAdmin 标志的用户端读取函数（clientapi 注入），
// 保证 client 平面无系统管理员短路。后台 Register 未先行完成时记录告警并跳过。
func RegisterClient(root *gin.RouterGroup, deps appdeps.Deps) {
	if sharedAPI == nil {
		log.Printf("sfunode: 用户端节点池路由挂载跳过（后台 Register 未先行完成）")
		return
	}
	handlers := &api{svc: sharedAPI.svc, hub: sharedAPI.hub, deps: deps, controlAddress: sharedAPI.controlAddress}
	guild := root.Group("/guilds/:guildID", deps.Auth)
	mount("client GET /guilds/:guildID/node-pool", func() { guild.GET("/node-pool", handlers.guildGetPool) })
	mount("client PUT /guilds/:guildID/node-pool", func() { guild.PUT("/node-pool", handlers.guildPutPool) })
}

// mount 挂载单条路由；若其他并行模块已注册同一 method+path（gin 会 panic），
// 捕获后跳过并告警，避免整个服务启动失败。路由归属冲突需在集成阶段人工裁决。
func mount(name string, register func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("sfunode: 路由 %s 已被其他模块注册，跳过挂载（需集成裁决）: %v", name, r)
		}
	}()
	register()
}
