// Package platformadmin 平台级用户治理（仅系统管理员）：
//   - 用户目录（搜索/分页）；
//   - 账号禁用/解禁（禁用即吊销全部会话，两认证平面登录/续期/鉴权均拒绝）;
//   - 管理员重置密码（吊销全部会话）;
//   - 系统管理员授予/回收（防自锁：不能操作自己、至少保留一名系统管理员）;
//   - 用户端注册开关落库（PlatformSetting，clientapi 读取 DB 优先、环境变量兜底）。
package platformadmin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
)

// Register 挂载 /api/v1/admin/users 与 /api/v1/admin/registration。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{deps: deps}

	admin := v1.Group("/admin", deps.Auth, h.requireSystemAdmin())
	admin.GET("/users", h.listUsers)
	admin.POST("/users/:userID/disable", h.disableUser)
	admin.POST("/users/:userID/enable", h.enableUser)
	admin.POST("/users/:userID/reset-password", h.resetPassword)
	admin.PATCH("/users/:userID/system-admin", h.patchSystemAdmin)
	admin.GET("/registration", h.getRegistration)
	admin.PUT("/registration", h.putRegistration)
	return nil
}

type api struct {
	deps appdeps.Deps
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return false
	}
	return true
}

// requireSystemAdmin 平台治理端点仅系统管理员可用（叠加在 deps.Auth 之后）。
func (h *api) requireSystemAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !h.deps.CurrentUser(c).SystemAdmin {
			fail(c, http.StatusForbidden, "MISSING_PERMISSION", "仅系统管理员可执行平台治理操作")
			c.Abort()
		}
	}
}
