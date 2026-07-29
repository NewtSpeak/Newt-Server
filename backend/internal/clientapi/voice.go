package clientapi

// 语音频道用户端端点接线：把 voice 包的用户级能力（进出房/续签/自我状态/RTT/
// 迁移确认/语音成员列表）挂到 /gapi/v1。
//
// 通过 init 给包内钩子 RegisterVoice 赋值（与 text.go 同一模式）：init 在任何
// Register 调用前必然执行，保证 server 装配时钩子已生效；实际挂载逻辑全部在
// voice.RegisterClient 中，本文件只做适配，clientapi 不感知语音编排内部实现。
//
// voice.Service 为全进程单例（迁移引擎 + 总线订阅只初始化一次）：后台
// voice.Register（/api/v1）先行构造，这里的挂载复用同一实例，详见
// internal/voice/register.go 的装配顺序说明。

import (
	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/voice"
)

func init() {
	RegisterVoice = func(authed *gin.RouterGroup, deps appdeps.Deps) {
		// deps 已由 clientapi.Register 换成用户端语义（Auth=aud:client 校验、
		// CurrentUser 读用户端上下文键），voice 包内经中间件适配读当前用户。
		voice.RegisterClient(authed, deps)
	}
	// 免认证公开端点（验签公钥）：挂用户端根组，用户端无需触达 /api/v1。
	RegisterVoicePublic = func(root *gin.RouterGroup, deps appdeps.Deps) {
		voice.RegisterClientPublic(root, deps)
	}
}
