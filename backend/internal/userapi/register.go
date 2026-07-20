// Package userapi 用户账号自助端点（Owl-Desktop docs 01 / 16），双认证平面共享 handler
//（模式对齐 internal/guildapi）：
//   - 后台（/api/v1，aud=admin）与用户端（/gapi/v1，aud=client）挂载同一套路由，
//     认证中间件与当前用户读取取 deps.Auth / deps.CurrentUser（各平面注入自己的实现）；
//   - 本包端点均为「本人操作自己的账号」或「查看公开资料」，不涉及 RBAC 层级，
//     两个平面语义一致（SystemAdmin 不产生任何特殊短路）。
//
// 路由清单：
//
//	GET    /users/@me                  当前用户完整资料
//	PATCH  /users/@me                  修改 display_name（1–32）/ bio（≤190）
//	POST   /users/@me/avatar           上传头像（multipart file 字段，≤8MB，png/jpeg/webp/gif）
//	DELETE /users/@me/avatar           移除头像
//	PATCH  /users/@me/password         修改密码（旧密码验证；吊销除当前会话外全部 refresh token）
//	GET    /users/@me/sessions         列出活跃登录会话（脱敏）
//	DELETE /users/@me/sessions/:id     吊销指定会话（id 为会话链 session_id）
//	GET    /users/@me/settings         读取服务端存储的用户设置 JSON 文档
//	PATCH  /users/@me/settings         按 top-level key 合并更新设置（值为 null 删除该 key）
//	PUT    /users/@me/settings         整体替换设置文档（204，触发 USER_SETTINGS_UPDATE）
//	GET    /users/:id                  公开资料（须与请求者共享至少一个 guild，否则 404）
package userapi

import (
	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/security"
)

// Register 挂载用户账号端点。group 为平面根组（/api/v1 或 /gapi/v1）。
func Register(group *gin.RouterGroup, deps appdeps.Deps) error {
	h := &api{
		deps:   deps,
		tokens: security.NewTokenManager(deps.Cfg.JWTSecret, deps.Cfg.AccessTokenTTL),
	}
	authed := group.Group("", deps.Auth)

	me := authed.Group("/users/@me")
	me.GET("", h.me)
	me.PATCH("", h.patchMe)
	// 账号删除（docs 16 FR-04 危险区）：密码确认；拥有服务器时 409。
	me.DELETE("", h.deleteAccount)
	me.POST("/avatar", h.uploadAvatar)
	me.DELETE("/avatar", h.deleteAvatar)
	me.PATCH("/password", h.changePassword)
	me.GET("/sessions", h.listSessions)
	me.DELETE("/sessions", h.revokeOtherSessions)
	me.DELETE("/sessions/:id", h.revokeSession)
	me.GET("/settings", h.getSettings)
	me.PATCH("/settings", h.patchSettings)
	me.PUT("/settings", h.putSettings)

	// 公开资料：gin 静态段 @me 优先于参数段 :id，两者可共存。
	authed.GET("/users/:id", h.publicProfile)
	return nil
}
