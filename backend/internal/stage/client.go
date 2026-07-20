package stage

// 用户端（/gapi/v1）挂载：复用后台同一 service 单例，仅认证平面与 URL 前缀不同。
//
// 设计要点：
//   - service 的一次性装配（权威裁决钩子、总线订阅、后台扫描）在 register.go 的
//     ensureService 中完成且全进程只执行一次，本文件只做第二前缀的路由挂载；
//   - handlers 按认证平面各建一份：仅 currentUser 读取函数不同（aud=client 读
//     clientapi 上下文键），业务逻辑与权限校验全部共用；
//   - 权限校验（RBAC 位 / 舞台角色 / Restriction / SystemAdmin）都在 handler 内
//     基于当前用户计算，与前缀无关：服主/协管用用户端凭证登录后照常可抱麦、
//     改舞台配置、结束他人共享；无权限的普通用户会被 handler 正常拒绝。

import (
	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
)

// RegisterClient 把舞台与屏幕共享端点挂到用户端认证平面（/gapi/v1，aud=client）。
//
// authed 为 clientapi 传入的已带用户端认证的路由组。
//
// 挂载范围（docs 11 §8.1、docs 14 §7.2）：
//   - 舞台用户端：apply / cancel-apply / self-leave / queue；
//   - 舞台管理操作：bring-up / bring-down / PATCH voice-stage——服主/协管在用户端
//     操作是常态，权限由 handler 内 RBAC（STAGE_BRING_UP 等）校验，不依赖前缀；
//   - 屏幕共享：start / stop / stop-user（STREAM_END_OTHERS 校验在 handler）、
//     GET /guilds/{gid}/screen-quota 配额查询。
//
// 不挂系统管配额治理端点（PATCH /admin/guilds/{gid}/screen-quota、
// PATCH /admin/screen-quota/settings）——平台治理保持仅后台（/api/v1）可达。
func RegisterClient(authed *gin.RouterGroup, deps appdeps.Deps) {
	svc := ensureService(deps)
	// 仅换用户端的当前用户读取函数（aud=client），handler 集合与后台共用。
	h := &handlers{svc: svc, currentUser: deps.CurrentUser}

	// authed 已带用户端认证中间件，这里无需再叠加 deps.Auth。
	channels := authed.Group("/channels/:channelID")
	channels.GET("/voice-stage", h.getVoiceStage)
	channels.PATCH("/voice-stage", h.patchVoiceStage)
	channels.GET("/stage/queue", h.getQueue)
	channels.DELETE("/stage/queue/:userID", h.removeFromQueue)
	channels.POST("/stage/apply", h.apply)
	channels.DELETE("/stage/apply", h.cancelApply)
	channels.POST("/stage/bring-up", h.bringUp)
	channels.POST("/stage/bring-down", h.bringDown)
	channels.POST("/stage/self-leave", h.selfLeave)
	channels.POST("/voice/screen/start", h.screenStart)
	channels.POST("/voice/screen/stop", h.screenStop)
	channels.POST("/voice/screen/stop-user", h.screenStopUser)
	authed.GET("/guilds/:guildID/screen-quota", h.guildScreenQuota)
}
