package clientapi

// 文本频道用户端端点接线：把 message 包的用户级能力（消息/编辑历史/附件/搜索/
// 反应/打字指示/语音包只读）挂到 /gapi/v1。
//
// 通过 init 给包内钩子 RegisterText 赋值：init 在任何 Register 调用前必然执行，
// 保证 server 装配时钩子已生效；实际挂载逻辑全部在 message.RegisterClient 中，
// 本文件只做适配，clientapi 不感知文本频道内部实现。

import (
	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/message"
)

func init() {
	RegisterText = func(root *gin.RouterGroup, deps appdeps.Deps) error {
		// deps 已由 clientapi.Register 换成用户端语义（Auth=aud:client 校验、
		// CurrentUser 读用户端上下文键），message 包内无需感知认证平面差异。
		return message.RegisterClient(root, deps)
	}
}
