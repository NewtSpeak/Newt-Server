package voice

// 用户端（/gapi/v1）挂载：复用后台同一 Service 单例，仅认证平面与 URL 前缀不同。
//
// 设计要点：
//   - Service 含迁移引擎与事件总线订阅，全进程仅一份（见 register.go ensureService），
//     本文件只做「同一实例、第二前缀」的路由挂载，不产生任何新的后台副作用；
//   - deps.Auth / deps.CurrentUser 必须为用户端（aud=client）语义，由 clientapi.Register
//     注入；handler 读当前用户经 injectCurrentUser 中间件适配，与前缀无关；
//   - 权限计算（perms.LoadGuild + RBAC + Restriction）在 handler 内完成，不依赖前缀。
//     SystemAdmin 在 perms.LoadGuild 中获得全权限——系统管理员用用户端凭证登录时
//     仍是系统管理员，符合语义，无需在用户端做额外拦截。

import (
	"log"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
)

// RegisterClient 把语音编排的用户级端点挂到用户端认证平面（/gapi/v1，aud=client）。
//
// authed 为 clientapi 传入的已带用户端认证的路由组。
//
// 只挂用户级端点；服级语音管理端点（disconnect / states）经
// RegisterClientModeration 用「剥离 SystemAdmin 的 CurrentUser」单独挂载，
// 平台级手动迁移（POST /admin/voice/migrations）保持仅后台（/api/v1）可达。
//
// 免认证公开端点（验签公钥）经 RegisterClientPublic 单独挂到用户端根组，
// 保证用户端流量完全不触达 /api/v1（含公开端点）。
func RegisterClient(authed *gin.RouterGroup, deps appdeps.Deps) {
	svc, err := ensureService(deps)
	if err != nil {
		// 装配顺序假设下后台 Register 已先行构造，此处仅在依赖缺失（MediaTokens
		// 未装配）时可能失败；记录后跳过挂载，不拖垮用户端其他领域的装配。
		log.Printf("voice: 用户端路由挂载失败: %v", err)
		return
	}
	// 认证平面适配：读当前用户改走 clientapi 的上下文键（aud=client）。
	group := authed.Group("", injectCurrentUser(deps.CurrentUser))

	// 会话生命周期：进房（含调度 + 经 mediatoken 签发含 sid 的 Media Token）/
	// 离房 / 续签 / 自我状态 / RTT 上报 / 热迁移确认（docs 05 / 09 / 10）。
	tryRegister(func() { group.POST("/voice/join", svc.handleJoin) })
	tryRegister(func() { group.POST("/voice/leave", svc.handleLeave) })
	tryRegister(func() { group.POST("/voice/refresh-token", svc.handleRefreshToken) })
	tryRegister(func() { group.PATCH("/voice/state", svc.handleSelfState) })
	tryRegister(func() { group.POST("/voice/rtt", svc.handleRTTReport) })
	// 客户端侧 ICE/连接失败上报（BI.3 提前判死独立信号源，docs 15 §5）。
	tryRegister(func() { group.POST("/voice/ice-failed", svc.handleIceFailed) })
	// ICE 失败上报（docs 13 FR-16 / 15 BI.2）：双信号提前判死的独立信号源。
	tryRegister(func() { group.POST("/voice/ice-failure", svc.handleICEFailure) })
	tryRegister(func() { group.POST("/voice/migrations/:migrationID/ack", svc.handleMigrationAck) })
	// 频道语音成员列表；不可见一律 404（docs 06 议题 8 防扫频语义，handler 内实现）。
	tryRegister(func() { group.GET("/guilds/:guildID/channels/:channelID/voice-states", svc.handleListVoiceStates) })
	// 候选节点池下发（docs 13 §7.1）：客户端后台 RTT 探测用，成员即可读。
	tryRegister(func() { group.GET("/guilds/:guildID/voice/nodes", svc.handleListVoiceNodes) })
}

// RegisterClientModeration 把服级语音管理端点挂到用户端认证平面：
//   - POST /guilds/{gid}/voice/disconnect（踢出语音，MOVE_MEMBERS/MUTE_MEMBERS + 层级）
//   - PATCH /guilds/{gid}/voice/states/{uid}（服务器静音/耳聋，MUTE/DEAFEN_MEMBERS + 层级）
//
// deps.CurrentUser 必须为「剥离 SystemAdmin 标志」的用户端读取函数（clientapi 注入）：
// 用户端平面一律走标准 RBAC + 层级校验，不给系统管理员短路。
func RegisterClientModeration(authed *gin.RouterGroup, deps appdeps.Deps) {
	svc, err := ensureService(deps)
	if err != nil {
		log.Printf("voice: 用户端管理路由挂载失败: %v", err)
		return
	}
	group := authed.Group("", injectCurrentUser(deps.CurrentUser))
	tryRegister(func() { group.POST("/guilds/:guildID/voice/disconnect", svc.handleAdminDisconnect) })
	// 管理员移动成员到另一语音频道（docs 09 FR-29：MOVE_MEMBERS + 层级）。
	tryRegister(func() { group.POST("/guilds/:guildID/voice/move", svc.handleAdminMove) })
	tryRegister(func() { group.PATCH("/guilds/:guildID/voice/states/:userID", svc.handleServerState) })
}

// RegisterClientPublic 把免认证公开端点（Media Token 验签公钥）挂到用户端根组，
// 让用户端/客户端工具无需触达后台前缀即可取公钥（用户端与后台地址隔离要求）。
func RegisterClientPublic(root *gin.RouterGroup, deps appdeps.Deps) {
	svc, err := ensureService(deps)
	if err != nil {
		log.Printf("voice: 用户端公开路由挂载失败: %v", err)
		return
	}
	tryRegister(func() { root.GET("/voice/public-key", svc.handlePublicKey) })
}
