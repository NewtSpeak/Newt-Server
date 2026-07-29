package clientapi

// 屏幕共享（及同包的舞台）用户端端点接线：把 stage 包的用户级能力挂到 /gapi/v1。
//
// 屏幕共享（start/stop/stop-user/配额查询）与舞台（apply/queue/抱麦/self-leave/
// voice-stage 配置）在后端同属 internal/stage 包、共用同一 service 单例与
// RegisterClient 入口，故经本钩子一并挂载；RegisterVoice（voice.go）只负责
// 语音会话编排端点，两钩子挂载的路径互不重叠。
//
// 通过 init 赋值（与 text.go 同一模式）；stage 的一次性装配（钩子/订阅/后台扫描）
// 由后台 stage.Register（/api/v1）先行完成，这里复用同一实例，详见
// internal/stage/register.go 的装配顺序说明。

import (
	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/stage"
)

func init() {
	RegisterScreen = func(authed *gin.RouterGroup, deps appdeps.Deps) {
		// deps 已由 clientapi.Register 换成用户端语义（Auth=aud:client 校验、
		// CurrentUser 读用户端上下文键），stage 的 handlers 直接以其读当前用户。
		stage.RegisterClient(authed, deps)
	}
}
