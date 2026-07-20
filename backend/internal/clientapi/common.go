package clientapi

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/owlspeak/owl-server/backend/internal/appdeps"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/security"
)

// clientUserContextKey 用户端自己的上下文键，与后台 httpapi 的键无关，
// 避免两个认证平面在同一进程内产生任何隐式耦合。
const clientUserContextKey = "client_authenticated_user"

// api 用户端 handler 集合。独立持有 TokenManager 与 LoginLimiter 实例：
// token 与后台共用同一 JWT secret（受众隔离靠 aud claim），限流状态与后台互不影响。
type api struct {
	deps          appdeps.Deps
	tokens        *security.TokenManager
	limiter       *security.LoginLimiter
	signupEnabled bool
}

func newAPI(deps appdeps.Deps, signupEnabled bool) *api {
	return &api{
		deps:          deps,
		tokens:        security.NewTokenManager(deps.Cfg.JWTSecret, deps.Cfg.AccessTokenTTL),
		limiter:       security.NewLoginLimiter(5, 20, 15*time.Minute),
		signupEnabled: signupEnabled,
	}
}

// requireAuth 用户端登录校验：仅接受 aud=client 的 access token。
// 后台（aud=admin）token 打用户端 API 一律 401，两个平面的凭证不可互通。
func (h *api) requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			fail(c, 401, "UNAUTHORIZED", "缺少访问令牌")
			c.Abort()
			return
		}
		userID, err := h.tokens.ParseAccessTokenWithAudience(strings.TrimPrefix(header, "Bearer "), security.AudienceClient)
		if err != nil {
			fail(c, 401, "UNAUTHORIZED", err.Error())
			c.Abort()
			return
		}
		var user model.User
		if err := h.deps.DB.First(&user, "id = ?", userID).Error; err != nil {
			fail(c, 401, "UNAUTHORIZED", "用户不存在")
			c.Abort()
			return
		}
		if user.Disabled() {
			fail(c, 401, "ACCOUNT_DISABLED", "账号已被平台禁用")
			c.Abort()
			return
		}
		c.Set(clientUserContextKey, user)
		c.Next()
	}
}

// currentUser 取当前登录用户（必须位于 requireAuth 之后的 handler 中调用）。
func currentUser(c *gin.Context) model.User { return c.MustGet(clientUserContextKey).(model.User) }

// CurrentUser 暴露给领域子模块（RegisterText/RegisterVoice/RegisterScreen 的实现方）
// 经 Deps.CurrentUser 使用，语义同后台的 httpapi.CurrentUser 但读取用户端上下文键。
func CurrentUser(c *gin.Context) model.User { return currentUser(c) }

// currentUserWithoutSystemAdmin 管理端点投影专用的当前用户读取：强制清除
// SystemAdmin 标志，使 perms.LoadGuild 等权限计算不产生任何系统管理员短路
//（client 平面语义：系统管理员用用户端凭证登录即普通用户）。
func currentUserWithoutSystemAdmin(c *gin.Context) model.User {
	user := currentUser(c)
	user.SystemAdmin = false
	return user
}

// errorResponse 错误响应格式与后台一致：{"error":{code,message}}。
type errorResponse struct {
	Error apiError `json:"error"`
}
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, errorResponse{apiError{code, message}})
}

func bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		fail(c, 400, "INVALID_REQUEST", err.Error())
		return false
	}
	return true
}
