// Package oauth 实现 NewtSpeak OAuth2 Authorization Server（设备码为主），
// 为 CLI / AI Agent 签发 aud=agent 的用户委托令牌。
package oauth

import (
	"github.com/gin-gonic/gin"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
)

// Register 挂载 /oauth/v1/* 路由到传入的根引擎 group（通常为 router.Group("") 或 /oauth/v1）。
func Register(root *gin.RouterGroup, deps appdeps.Deps) error {
	h := newHandler(deps)
	v1 := root.Group("/oauth/v1")

	// 公开
	v1.POST("/device/code", h.postDeviceCode)
	v1.GET("/device/:user_code", h.getDeviceInfo)
	v1.POST("/token", h.postToken)
	v1.POST("/revoke", h.postRevoke)
	v1.GET("/.well-known/oauth-authorization-server", h.metadata)
	v1.GET("/userinfo", h.userinfo)

	// 需用户端（aud=client）登录：Desktop/Web 授权页调用
	v1.POST("/device/approve", h.postDeviceApprove)
	v1.POST("/device/deny", h.postDeviceDeny)
	v1.POST("/authorize/approve", h.postAuthorizeApprove) // PKCE 同意 → code
	v1.GET("/grants", h.listGrants)
	v1.DELETE("/grants/:sessionID", h.revokeGrant)
	v1.POST("/grants/revoke-all", h.revokeAllGrants)

	return nil
}
