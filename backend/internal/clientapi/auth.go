package clientapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/platformadmin"
	"github.com/owlspeak/owl-server/backend/internal/security"
	"gorm.io/gorm"
)

type signupRequest struct {
	Username string `json:"username" binding:"required,min=2,max=32"`
	Email    string `json:"email" binding:"required,email,max=255"`
	Password string `json:"password" binding:"required,min=8,max=128"`
}

// signup POST /gapi/v1/auth/signup：用户端开放注册（与后台「仅首个用户」的初始化注册无关）。
// 新用户一律为普通用户（SystemAdmin=false）；开关权威来源为 PlatformSetting 落库值
//（控制台可改、即时生效），无记录时回退 CLIENT_SIGNUP_ENABLED 环境变量。
func (h *api) signup(c *gin.Context) {
	if !platformadmin.ClientSignupEnabled(h.deps.DB, h.signupEnabled) {
		fail(c, http.StatusForbidden, "SIGNUP_DISABLED", "注册暂未开放")
		return
	}
	var input signupRequest
	if !bind(c, &input) {
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	hash, err := security.HashPassword(input.Password)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_PASSWORD", err.Error())
		return
	}
	user := model.User{ID: uuid.New(), Username: input.Username, Email: input.Email, PasswordHash: hash, SystemAdmin: false}
	if err := h.deps.DB.Create(&user).Error; err != nil {
		// 唯一性靠 users 表的唯一索引兜底（TranslateError 已开启）。
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			fail(c, http.StatusConflict, "ACCOUNT_EXISTS", "用户名或邮箱已被使用")
			return
		}
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建账号失败")
		return
	}
	h.issueTokens(c, http.StatusCreated, user)
}

type loginRequest struct {
	Identifier string `json:"identifier" binding:"required"`
	Password   string `json:"password" binding:"required"`
}

// login POST /gapi/v1/auth/login：任何账号（含系统管理员）均可在用户端登录，
// 但只签发 aud=client 的凭证——系统管理员在用户端也只是普通用户身份。
func (h *api) login(c *gin.Context) {
	var input loginRequest
	if !bind(c, &input) {
		return
	}
	identifier := strings.ToLower(strings.TrimSpace(input.Identifier))
	if allowed, retryAfter := h.limiter.Allow(c.ClientIP(), identifier); !allowed {
		seconds := int(retryAfter.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		c.Header("Retry-After", strconv.Itoa(seconds))
		fail(c, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "登录尝试过多，请稍后再试")
		return
	}
	var user model.User
	if err := h.deps.DB.Where("LOWER(email) = ? OR LOWER(username) = ?", identifier, identifier).First(&user).Error; err != nil || !security.VerifyPassword(user.PasswordHash, input.Password) {
		h.limiter.Failure(c.ClientIP(), identifier)
		fail(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "账号或密码错误")
		return
	}
	// 平台禁用拦截（platformadmin）：凭证正确但账号被禁用，明确告知。
	if user.Disabled() {
		fail(c, http.StatusForbidden, "ACCOUNT_DISABLED", "账号已被平台禁用")
		return
	}
	h.limiter.Success(identifier)
	h.issueTokens(c, http.StatusOK, user)
}

type refreshRequest struct {
	Token string `json:"refresh_token" binding:"required"`
}

// refresh POST /gapi/v1/auth/refresh：refresh token 轮换（旧令牌吊销、签发新对）。
// 仅接受用户端受众（client）的 refresh token，后台 refresh token 在此无效。
func (h *api) refresh(c *gin.Context) {
	var input refreshRequest
	if !bind(c, &input) {
		return
	}
	var stored model.RefreshToken
	err := h.deps.DB.Where("token_hash = ? AND audience = ? AND revoked_at IS NULL AND expires_at > ?", security.HashToken(input.Token), security.AudienceClient, time.Now().UTC()).First(&stored).Error
	if err != nil {
		fail(c, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "刷新令牌无效或已过期")
		return
	}
	now := time.Now().UTC()
	if err := h.deps.DB.Model(&stored).Update("revoked_at", now).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "刷新令牌轮换失败")
		return
	}
	var user model.User
	// 轮换时复查禁用状态：被平台禁用的账号无法续期用户端凭证。
	if err := h.deps.DB.First(&user, "id = ?", stored.UserID).Error; err != nil || user.Disabled() {
		fail(c, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "刷新令牌无效")
		return
	}
	// 轮换后的新 token 继承会话链（session_id / 会话创建时间）。
	h.issueTokensForSession(c, http.StatusOK, user, stored.SessionID, stored.SessionCreatedAt)
}

// logout POST /gapi/v1/auth/logout：吊销用户端 refresh token（幂等）。
func (h *api) logout(c *gin.Context) {
	var input refreshRequest
	if !bind(c, &input) {
		return
	}
	now := time.Now().UTC()
	h.deps.DB.Model(&model.RefreshToken{}).Where("token_hash = ? AND audience = ? AND revoked_at IS NULL", security.HashToken(input.Token), security.AudienceClient).Update("revoked_at", now)
	c.Status(http.StatusNoContent)
}

type tokenResponse struct {
	AccessToken      string     `json:"access_token"`
	RefreshToken     string     `json:"refresh_token"`
	AccessExpiresAt  time.Time  `json:"access_expires_at"`
	RefreshExpiresAt time.Time  `json:"refresh_expires_at"`
	User             model.User `json:"user"`
}

// issueTokens 签发用户端（aud=client）access/refresh token 对并落库（登录/注册开启新会话链）。
func (h *api) issueTokens(c *gin.Context, status int, user model.User) {
	h.issueTokensForSession(c, status, user, uuid.New(), time.Now().UTC())
}

// issueTokensForSession 按指定会话链签发 token 对（refresh 轮换时继承旧链）。
func (h *api) issueTokensForSession(c *gin.Context, status int, user model.User, sessionID uuid.UUID, sessionCreatedAt time.Time) {
	access, accessExpiry, err := h.tokens.AccessTokenForSession(user.ID, security.AudienceClient, sessionID.String())
	if err != nil {
		fail(c, http.StatusInternalServerError, "TOKEN_ERROR", "签发访问令牌失败")
		return
	}
	refresh, refreshHash, err := security.NewRefreshToken()
	if err != nil {
		fail(c, http.StatusInternalServerError, "TOKEN_ERROR", "签发刷新令牌失败")
		return
	}
	refreshExpiry := time.Now().UTC().Add(h.deps.Cfg.RefreshTokenTTL)
	stored := model.RefreshToken{
		ID: uuid.New(), UserID: user.ID, TokenHash: refreshHash, Audience: security.AudienceClient,
		SessionID: sessionID, SessionCreatedAt: sessionCreatedAt, ExpiresAt: refreshExpiry,
	}
	if err := h.deps.DB.Create(&stored).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存刷新令牌失败")
		return
	}
	c.JSON(status, tokenResponse{access, refresh, accessExpiry, refreshExpiry, user})
}
