package message

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
)

// TestTypingAllowed 打字指示权限纯逻辑：需 SEND_MESSAGES，与其他权限位无关。
func TestTypingAllowed(t *testing.T) {
	cases := []struct {
		name string
		bits rbac.Permission
		want bool
	}{
		{"有发送权限", rbac.SendMessages, true},
		{"发送叠加其他权限", rbac.SendMessages | rbac.ViewChannel | rbac.AddReactions, true},
		{"无任何权限", 0, false},
		{"仅可见不可发（Restriction 禁发收紧后典型状态）", rbac.ViewChannel | rbac.ReadMessageHistory, false},
		{"仅管理权限也不放行", rbac.ManageMessages | rbac.ManageChannels, false},
	}
	for _, tc := range cases {
		if got := typingAllowed(tc.bits); got != tc.want {
			t.Errorf("%s: typingAllowed=%v，期待 %v", tc.name, got, tc.want)
		}
	}
}

// rejectAuth 模拟认证中间件的未登录分支：无凭证一律 401（与两个平面的
// 真实中间件在「无 Authorization 头」时的行为一致），不触达数据库。
func rejectAuth(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, errorResponse{apiError{"UNAUTHORIZED", "缺少访问令牌"}})
}

// newDualPrefixRouter 在同一 router 树上同时挂后台（/api/v1）与用户端（/gapi/v1）
// 两个平面的全部消息路由，复现生产装配拓扑。service 不注入数据库：本冒烟只验证
// 路由注册（gin 通配符冲突会在注册期 panic）与认证中间件生效，请求不会触达 handler 内部。
func newDualPrefixRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	backend := &service{urlPrefix: "/api/v1"}
	client := &service{urlPrefix: "/gapi/v1"}
	backend.mountBackend(router.Group("/api/v1"), rejectAuth)
	client.mountClient(router.Group("/gapi/v1"), rejectAuth)
	return router
}

// TestDualPrefixCoexistSmoke 双前缀共存冒烟：
//   - 后台 mountBackend 与用户端 mountClient 挂同一 router 不 panic（无路由/通配符冲突）；
//   - 两个前缀下的受保护端点未认证一律 401；
//   - 用户端不挂管理端点（404 而非 401，路由不存在）；
//   - 签名下载端点位于认证之外（无凭证返回 404 签名校验失败，而非 401）。
func TestDualPrefixCoexistSmoke(t *testing.T) {
	router := newDualPrefixRouter(t)
	channelID := uuid.NewString()
	guildID := uuid.NewString()
	attachmentID := uuid.NewString()

	authedCases := []struct{ method, path string }{
		{http.MethodPost, "/gapi/v1/channels/" + channelID + "/messages"},
		{http.MethodGet, "/gapi/v1/channels/" + channelID + "/messages"},
		{http.MethodGet, "/gapi/v1/channels/" + channelID + "/messages/123"},
		{http.MethodPatch, "/gapi/v1/channels/" + channelID + "/messages/123"},
		{http.MethodDelete, "/gapi/v1/channels/" + channelID + "/messages/123"},
		{http.MethodGet, "/gapi/v1/channels/" + channelID + "/messages/123/edits"},
		{http.MethodPut, "/gapi/v1/channels/" + channelID + "/messages/123/reactions/x/@me"},
		{http.MethodDelete, "/gapi/v1/channels/" + channelID + "/messages/123/reactions/x/@me"},
		{http.MethodPost, "/gapi/v1/channels/" + channelID + "/attachments/presign"},
		{http.MethodPut, "/gapi/v1/attachments/" + attachmentID + "/content"},
		{http.MethodGet, "/gapi/v1/search/messages"},
		{http.MethodGet, "/gapi/v1/guilds/" + guildID + "/voice-pack"},
		{http.MethodGet, "/gapi/v1/guilds/" + guildID + "/channels/" + channelID + "/voice-pack"},
		// 语音包配置写入口双平面同挂（docs 03 FR-35：服管/频道管理员在客户端配置；
		// MANAGE_GUILD / MANAGE_CHANNELS 由 handler 内校验）。
		{http.MethodPatch, "/gapi/v1/guilds/" + guildID + "/voice-pack"},
		{http.MethodPut, "/gapi/v1/guilds/" + guildID + "/channels/" + channelID + "/voice-pack"},
		{http.MethodPost, "/gapi/v1/channels/" + channelID + "/typing"},
		// 后台平面抽查：同一 handler 集挂在 /api/v1 且同样受认证保护。
		{http.MethodPost, "/api/v1/channels/" + channelID + "/messages"},
		{http.MethodGet, "/api/v1/search/messages"},
		{http.MethodPatch, "/api/v1/admin/guilds/" + guildID + "/upload-limit"},
	}
	for _, tc := range authedCases {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 未认证返回 %d，期待 401", tc.method, tc.path, rec.Code)
		}
	}

	// 管理端点不挂用户端前缀：路由不存在（gin 返回 404），而非 401（存在但未认证）。
	adminOnlyCases := []struct{ method, path string }{
		{http.MethodPatch, "/gapi/v1/admin/guilds/" + guildID + "/upload-limit"},
		{http.MethodGet, "/gapi/v1/guilds/" + guildID + "/message-retention"},
		{http.MethodPatch, "/gapi/v1/guilds/" + guildID + "/message-retention"},
		// typing 为用户端专属，后台前缀不挂。
		{http.MethodPost, "/api/v1/channels/" + channelID + "/typing"},
	}
	for _, tc := range adminOnlyCases {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s 不应挂载在该前缀（返回 401 说明路由存在）", tc.method, tc.path)
		}
	}

	// 签名下载在认证之外：无凭证也不 401，缺失/非法签名走 404（防扫频语义）。
	for _, path := range []string{
		"/gapi/v1/attachments/" + attachmentID,
		"/api/v1/attachments/" + attachmentID,
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s 无签名返回 %d，期待 404", path, rec.Code)
		}
	}
}
