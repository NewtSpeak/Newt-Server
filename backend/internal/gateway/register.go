// Package gateway 客户端实时通道（WebSocket）：认证、事件订阅与按可见性过滤的推送
//（docs 05 §11 事件表；docs 06 议题 8 的 404/不可见语义在推送侧同样生效）。
package gateway

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/security"
)

// memberCacheTTL guild 成员列表的广播缓存时长（短 TTL，容忍成员变动的秒级延迟）。
const memberCacheTTL = 30 * time.Second

// Register 挂载后台 GET /gateway WebSocket 端点并订阅事件总线（仅接受 aud=admin token）。
// 端点不加 Auth 中间件（浏览器 WS 无法自定义 header），认证由首条 IDENTIFY 帧完成，
// token 校验与 httpapi 共用同一 JWT secret，结果一致。
func Register(v1 *gin.RouterGroup, deps appdeps.Deps) error {
	return RegisterWithAudience(v1, deps, security.AudienceAdmin)
}

// RegisterWithAudience 在指定路由组挂载 GET /gateway，IDENTIFY 阶段仅接受指定受众的
// access token。用户端（clientapi）以 aud=client 复用同一套 WS 协议实现；
// 每次调用创建独立的 hub 与事件订阅，两个平面的连接注册表互不影响。
func RegisterWithAudience(group *gin.RouterGroup, deps appdeps.Deps, audience string) error {
	tokens := security.NewTokenManager(deps.Cfg.JWTSecret, deps.Cfg.AccessTokenTTL)
	h := newHandler(
		&dbAuthenticator{db: deps.DB, tokens: tokens, audience: audience},
		newMemberCache(&dbDirectory{db: deps.DB}, memberCacheTTL),
		defaultOptions(),
	)
	if deps.Presence != nil {
		h.attachPresence(deps.Presence)
	}
	deps.Bus.Subscribe(h.hub.dispatch)
	group.GET("/gateway", h.serve)
	return nil
}

// AuthenticatorFunc 把函数适配为 IDENTIFY 认证器（bot 开放平面注入 bot token 校验）。
type AuthenticatorFunc func(token string) (model.User, []uuid.UUID, error)

// Authenticate 实现 authenticator 接口。
func (f AuthenticatorFunc) Authenticate(token string) (model.User, []uuid.UUID, error) {
	return f(token)
}

// RegisterWithAuthenticator 以自定义认证器挂载 GET /gateway（bot 开放平面使用：
// IDENTIFY 帧携带 bot token 而非 JWT）。事件推送路径与其他平面完全一致——
// bot 作为 guild 成员按可见性接收 MESSAGE_* / VOICE_* / MESSAGE_STREAM_* 等事件。
func RegisterWithAuthenticator(group *gin.RouterGroup, deps appdeps.Deps, auth AuthenticatorFunc) error {
	h := newHandler(
		auth,
		newMemberCache(&dbDirectory{db: deps.DB}, memberCacheTTL),
		defaultOptions(),
	)
	if deps.Presence != nil {
		h.attachPresence(deps.Presence)
	}
	deps.Bus.Subscribe(h.hub.dispatch)
	group.GET("/gateway", h.serve)
	return nil
}
