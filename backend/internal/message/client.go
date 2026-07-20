package message

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
)

// 用户端（/gapi/v1）挂载：复用后台同一套 service/handler，仅认证平面与 URL 前缀不同。

// RegisterClient 把文本频道的用户级端点挂到用户端前缀下。
//
// root 应为用户端根组（如 /gapi/v1）：签名下载端点无需登录态（<img>/<video> 等
// 无鉴权头场景），必须挂在认证中间件之外，其余端点经 deps.Auth 建认证子组。
// deps.Auth / deps.CurrentUser 必须为用户端（aud=client）语义，由 clientapi.Register 注入。
//
// 响应中生成的 upload_url/download_url 前缀取自 root.BasePath()，
// 用户端上下文中绝不会出现 /api/v1 字样（防止用户端流量推断后台地址）。
//
// 不含任何管理端点（upload-limit、保留策略、语音包配置管理），
// 也不启动 GC/保留策略清理任务——后台任务由 Register 的实例统一负责，避免双跑。
func RegisterClient(root *gin.RouterGroup, deps appdeps.Deps) error {
	svc, err := newService(deps, root.BasePath())
	if err != nil {
		return err
	}
	svc.mountClient(root, deps.Auth)
	return nil
}

// mountClient 用户端平面全部路由：用户级端点 + 打字指示；auth 为用户端认证中间件。
func (s *service) mountClient(root *gin.RouterGroup, auth gin.HandlerFunc) {
	authed := root.Group("", auth)
	s.mountUserRoutes(root, authed)
	// 频道内打字指示（用户端典型场景）。
	authed.POST("/channels/:channelID/typing", s.postTyping)
}

// typingAllowed 打字指示权限：与发送消息一致（SEND_MESSAGES；
// Restriction 禁发已在权限位计算中收紧，无需额外判断）。
func typingAllowed(bits rbac.Permission) bool {
	return rbac.Has(bits, rbac.SendMessages)
}

// postTyping POST /channels/{id}/typing：发布 TYPING_START 事件，
// Gateway 按频道可见性过滤后下发给频道内其他客户端；本端点无响应体（204）。
// 频道不可见一律 404（防扫频），可见但无发送权限 403。
func (s *service) postTyping(c *gin.Context) {
	channelID, ok := parseUUIDParam(c, "channelID")
	if !ok {
		return
	}
	_, channel, bits, ok := s.channelAccess(c, channelID)
	if !ok {
		return
	}
	if !typingAllowed(bits) {
		fail(c, http.StatusForbidden, "MISSING_PERMISSION", "缺少发送消息权限")
		return
	}
	user := s.currentUser(c)
	s.bus.Publish(eventbus.Event{
		Type:      eventbus.EventTypingStart,
		GuildID:   &channel.GuildID,
		ChannelID: &channel.ID,
		Payload: gin.H{
			"channel_id": channel.ID,
			"guild_id":   channel.GuildID,
			"user_id":    user.ID,
			"timestamp":  time.Now().UTC(),
		},
	})
	c.Status(http.StatusNoContent)
}
