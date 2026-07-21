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
	"github.com/owlspeak/owl-server/backend/internal/moderation"
	"github.com/owlspeak/owl-server/backend/internal/platformadmin"
	"github.com/owlspeak/owl-server/backend/internal/platformbadge"
	"github.com/owlspeak/owl-server/backend/internal/registrationinvite"
	"github.com/owlspeak/owl-server/backend/internal/security"
	"gorm.io/gorm"
)

type signupRequest struct {
	Username string `json:"username" binding:"required,min=2,max=32"`
	Email    string `json:"email" binding:"required,email,max=255"`
	Password string `json:"password" binding:"required,min=8,max=128"`
	// InviteCode 平台注册邀请码（可选）：有效时无论注册开关状态均放行注册，
	// 注册成功后与用户创建同事务原子消耗次数（registrationinvite 包）。
	InviteCode string `json:"invite_code" binding:"omitempty,max=64"`
	// GuildInviteCode 社区（guild）邀请码（可选）：有效时同样绕过注册开关放行注册，
	// 但注册本身不消耗次数——次数由注册成功后客户端调用
	// POST /gapi/v1/invites/{code}/join 加入社区时消耗（含 Ban 拦截与事件广播）。
	// 与 invite_code 同时提供时以 invite_code（注册邀请）优先，本字段被忽略。
	GuildInviteCode string `json:"guild_invite_code" binding:"omitempty,max=64"`
}

// errRegistrationInviteExhausted 并发竞争下注册邀请次数被用尽（对外 403 INVITE_EXPIRED）。
var errRegistrationInviteExhausted = errors.New("注册邀请使用次数已用尽")

// signup POST /gapi/v1/auth/signup：用户端开放注册（与后台「仅首个用户」的初始化注册无关）。
// 新用户一律为普通用户（SystemAdmin=false）；开关权威来源为 PlatformSetting 落库值
//（控制台可改、即时生效），无记录时回退 CLIENT_SIGNUP_ENABLED 环境变量。
// 携带有效注册邀请码（invite_code）或社区邀请码（guild_invite_code）时绕过开关放行，
// 邀请码无效/失效返回 403；两者同时提供以 invite_code 优先。
func (h *api) signup(c *gin.Context) {
	var input signupRequest
	if !bind(c, &input) {
		return
	}
	var invite *model.RegistrationInvite
	if code := strings.TrimSpace(input.InviteCode); code != "" {
		resolved, status := registrationinvite.Resolve(h.deps.DB, code)
		switch status {
		case registrationinvite.StatusActive:
			invite = &resolved
		case registrationinvite.StatusNotFound:
			fail(c, http.StatusForbidden, "INVITE_INVALID", "注册邀请码无效")
			return
		default:
			fail(c, http.StatusForbidden, "INVITE_EXPIRED", "注册邀请码已过期或失效")
			return
		}
	} else if code := strings.TrimSpace(input.GuildInviteCode); code != "" {
		// 社区邀请码放行注册：仅校验有效性、不在此消耗次数（次数由注册后
		// 客户端调用 POST /invites/{code}/join 加入时原子消耗）。
		// ResolveActiveInvite 把「不存在」与「已过期」统一为 StatusNotFound
		//（join/预览的防泄露语义），但过期时仍返回已加载的记录（ID 非零），
		// 借此区分：不存在 → INVITE_INVALID，过期/用尽 → INVITE_EXPIRED。
		guildInvite, status := moderation.ResolveActiveInvite(h.deps.DB, code)
		switch {
		case status == 0:
			// 有效，绕过注册开关放行。
		case status == http.StatusGone || guildInvite.ID != uuid.Nil:
			fail(c, http.StatusForbidden, "INVITE_EXPIRED", "社区邀请码已过期或失效")
			return
		default:
			fail(c, http.StatusForbidden, "INVITE_INVALID", "社区邀请码无效")
			return
		}
	} else if !platformadmin.ClientSignupEnabled(h.deps.DB, h.signupEnabled) {
		fail(c, http.StatusForbidden, "SIGNUP_DISABLED", "注册暂未开放")
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
	// 邀请码消耗与用户创建同事务：并发竞争下不超用（ConsumeUse 条件更新原子判定）。
	err = h.deps.DB.Transaction(func(tx *gorm.DB) error {
		if invite != nil {
			consumed, err := registrationinvite.ConsumeUse(tx, invite.ID)
			if err != nil {
				return err
			}
			if !consumed {
				return errRegistrationInviteExhausted
			}
		}
		return tx.Create(&user).Error
	})
	if err != nil {
		if errors.Is(err, errRegistrationInviteExhausted) {
			fail(c, http.StatusForbidden, "INVITE_EXPIRED", "注册邀请码已过期或失效")
			return
		}
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

// login POST /gapi/v1/auth/login：任何账号（含系统所有者）均可在用户端登录，
// 签发 aud=client 凭证。系统所有者（system_admin）在登录响应中附带徽章，
// 并在用户端享有全部服务器管理权限（docs 04 FR-32）。
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
	AccessToken      string               `json:"access_token"`
	RefreshToken     string               `json:"refresh_token"`
	AccessExpiresAt  time.Time            `json:"access_expires_at"`
	RefreshExpiresAt time.Time            `json:"refresh_expires_at"`
	User             platformbadge.UserView `json:"user"`
}

// issueTokens 签发用户端（aud=client）access/refresh token 对并落库（登录/注册开启新会话链）。
func (h *api) issueTokens(c *gin.Context, status int, user model.User) {
	h.issueTokensForSession(c, status, user, uuid.New(), time.Now().UTC())
}

// issueTokensForSession 按指定会话链签发 token 对（refresh 轮换时继承旧链）。
// 系统所有者登录时 user.badges 自动附带「系统所有者」徽章。
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
	// 会话设备元数据（docs 01 FR-27）：登录/轮换请求即来自该设备，按当前请求采集。
	device, platform := security.DeviceInfo(c.GetHeader("User-Agent"))
	stored := model.RefreshToken{
		ID: uuid.New(), UserID: user.ID, TokenHash: refreshHash, Audience: security.AudienceClient,
		SessionID: sessionID, SessionCreatedAt: sessionCreatedAt, ExpiresAt: refreshExpiry,
		DeviceName: device, Platform: platform, IPAddress: c.ClientIP(),
	}
	if err := h.deps.DB.Create(&stored).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存刷新令牌失败")
		return
	}
	c.JSON(status, tokenResponse{
		AccessToken: access, RefreshToken: refresh,
		AccessExpiresAt: accessExpiry, RefreshExpiresAt: refreshExpiry,
		User: platformbadge.ViewOf(user),
	})
}
