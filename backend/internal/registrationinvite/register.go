// Package registrationinvite 平台注册邀请链接（仅系统管理员签发）：
//   - 管理 API（/api/v1/admin/registration-invites）：生成/列表/撤销邀请短码，
//     返回体附分享链接与客户端深链，控制台一键复制分发；
//   - 公开预检（/invite-api/registration/{code}）：桌面客户端在注册前免登录
//     校验邀请码有效性（无效 404、过期/用尽/撤销 410，不泄露细节）；
//   - clientapi.signup 凭有效邀请码可绕过用户端注册开关（client_signup_enabled），
//     注册成功后与用户创建同事务原子消耗次数（Resolve / ConsumeUse 供其调用）。
package registrationinvite

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
)

// Register 挂载后台管理 API /api/v1/admin/registration-invites（仅系统管理员）。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}

	admin := v1.Group("/admin/registration-invites", deps.Auth, h.requireSystemAdmin())
	admin.POST("", h.createInvite)
	admin.GET("", h.listInvites)
	admin.DELETE("/:inviteID", h.revokeInvite)
	return nil
}

// RegisterPublic 挂载公开预检端点（/invite-api，无需登录）：
// GET /registration/{code} 供桌面客户端在注册前校验邀请码。
func RegisterPublic(pub *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}
	pub.GET("/registration/:code", h.publicInfo)
	return nil
}

type api struct {
	deps appdeps.Deps
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

// requireSystemAdmin 注册邀请为平台级治理能力，仅系统管理员可用（叠加在 deps.Auth 之后）。
func (h *api) requireSystemAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !h.deps.CurrentUser(c).SystemAdmin {
			fail(c, http.StatusForbidden, "MISSING_PERMISSION", "仅系统管理员可管理注册邀请")
			c.Abort()
		}
	}
}
