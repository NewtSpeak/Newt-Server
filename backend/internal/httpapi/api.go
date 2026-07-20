package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/rbac"
	"github.com/owlspeak/owl-server/backend/internal/security"
	"gorm.io/gorm"
)

const userContextKey = "authenticated_user"

type API struct {
	db              *gorm.DB
	tokens          *security.TokenManager
	refreshTokenTTL time.Duration
}

func New(db *gorm.DB, tokens *security.TokenManager, refreshTokenTTL time.Duration) *API {
	return &API{db: db, tokens: tokens, refreshTokenTTL: refreshTokenTTL}
}

func (a *API) RegisterRoutes(group *gin.RouterGroup) {
	auth := group.Group("/auth")
	auth.POST("/register", a.register)
	auth.POST("/login", a.login)
	auth.POST("/refresh", a.refresh)
	auth.POST("/logout", a.logout)
	auth.GET("/me", a.requireAuth(), a.me)

	protected := group.Group("")
	protected.Use(a.requireAuth())
	protected.POST("/guilds", a.createGuild)
	protected.GET("/guilds/:guildID/roles", a.listRoles)
	protected.POST("/guilds/:guildID/roles", a.createRole)
	protected.PATCH("/guilds/:guildID/roles/:roleID", a.updateRole)
	protected.PUT("/guilds/:guildID/members/:memberID/roles/:roleID", a.assignRole)
	protected.DELETE("/guilds/:guildID/members/:memberID/roles/:roleID", a.removeRole)
	protected.POST("/guilds/:guildID/channels", a.createChannel)
	protected.PUT("/guilds/:guildID/channels/:channelID/overwrites/:targetID", a.upsertOverwrite)
	protected.GET("/guilds/:guildID/permissions/@me", a.myGuildPermissions)
	protected.GET("/guilds/:guildID/channels/:channelID/permissions/@me", a.myChannelPermissions)
}

type registerRequest struct {
	Username string `json:"username" binding:"required,min=2,max=32"`
	Email    string `json:"email" binding:"required,email,max=255"`
	Password string `json:"password" binding:"required,min=8,max=128"`
}

// register godoc
// @Summary 注册账号
// @Tags 认证
// @Accept json
// @Produce json
// @Param body body registerRequest true "注册资料"
// @Success 201 {object} tokenResponse
// @Failure 400 {object} errorResponse
// @Router /auth/register [post]
func (a *API) register(c *gin.Context) {
	var input registerRequest
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
	user := model.User{ID: uuid.New(), Username: input.Username, Email: input.Email, PasswordHash: hash}
	if err := a.db.Create(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			fail(c, http.StatusConflict, "ACCOUNT_EXISTS", "用户名或邮箱已被使用")
			return
		}
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建账号失败")
		return
	}
	a.issueTokens(c, http.StatusCreated, user)
}

type loginRequest struct {
	Identifier string `json:"identifier" binding:"required"`
	Password   string `json:"password" binding:"required"`
}

// login godoc
// @Summary 登录
// @Tags 认证
// @Accept json
// @Produce json
// @Param body body loginRequest true "用户名或邮箱及密码"
// @Success 200 {object} tokenResponse
// @Failure 401 {object} errorResponse
// @Router /auth/login [post]
func (a *API) login(c *gin.Context) {
	var input loginRequest
	if !bind(c, &input) {
		return
	}
	identifier := strings.ToLower(strings.TrimSpace(input.Identifier))
	var user model.User
	if err := a.db.Where("LOWER(email) = ? OR LOWER(username) = ?", identifier, identifier).First(&user).Error; err != nil || !security.VerifyPassword(user.PasswordHash, input.Password) {
		fail(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "账号或密码错误")
		return
	}
	a.issueTokens(c, http.StatusOK, user)
}

type refreshRequest struct {
	Token string `json:"refresh_token" binding:"required"`
}

func (a *API) refresh(c *gin.Context) {
	var input refreshRequest
	if !bind(c, &input) {
		return
	}
	var stored model.RefreshToken
	err := a.db.Where("token_hash = ? AND revoked_at IS NULL AND expires_at > ?", security.HashToken(input.Token), time.Now().UTC()).First(&stored).Error
	if err != nil {
		fail(c, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "刷新令牌无效或已过期")
		return
	}
	now := time.Now().UTC()
	if err := a.db.Model(&stored).Update("revoked_at", now).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "刷新令牌轮换失败")
		return
	}
	var user model.User
	if err := a.db.First(&user, "id = ?", stored.UserID).Error; err != nil {
		fail(c, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "刷新令牌无效")
		return
	}
	a.issueTokens(c, http.StatusOK, user)
}

func (a *API) logout(c *gin.Context) {
	var input refreshRequest
	if !bind(c, &input) {
		return
	}
	now := time.Now().UTC()
	a.db.Model(&model.RefreshToken{}).Where("token_hash = ? AND revoked_at IS NULL", security.HashToken(input.Token)).Update("revoked_at", now)
	c.Status(http.StatusNoContent)
}

func (a *API) me(c *gin.Context) { c.JSON(http.StatusOK, currentUser(c)) }

type tokenResponse struct {
	AccessToken      string     `json:"access_token"`
	RefreshToken     string     `json:"refresh_token"`
	AccessExpiresAt  time.Time  `json:"access_expires_at"`
	RefreshExpiresAt time.Time  `json:"refresh_expires_at"`
	User             model.User `json:"user"`
}

func (a *API) issueTokens(c *gin.Context, status int, user model.User) {
	access, accessExpiry, err := a.tokens.AccessToken(user.ID)
	if err != nil {
		fail(c, 500, "TOKEN_ERROR", "签发访问令牌失败")
		return
	}
	refresh, refreshHash, err := security.NewRefreshToken()
	if err != nil {
		fail(c, 500, "TOKEN_ERROR", "签发刷新令牌失败")
		return
	}
	refreshExpiry := time.Now().UTC().Add(a.refreshTokenTTL)
	stored := model.RefreshToken{ID: uuid.New(), UserID: user.ID, TokenHash: refreshHash, ExpiresAt: refreshExpiry}
	if err := a.db.Create(&stored).Error; err != nil {
		fail(c, 500, "DATABASE_ERROR", "保存刷新令牌失败")
		return
	}
	c.JSON(status, tokenResponse{access, refresh, accessExpiry, refreshExpiry, user})
}

func (a *API) requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			fail(c, 401, "UNAUTHORIZED", "缺少访问令牌")
			c.Abort()
			return
		}
		userID, err := a.tokens.ParseAccessToken(strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			fail(c, 401, "UNAUTHORIZED", err.Error())
			c.Abort()
			return
		}
		var user model.User
		if err := a.db.First(&user, "id = ?", userID).Error; err != nil {
			fail(c, 401, "UNAUTHORIZED", "用户不存在")
			c.Abort()
			return
		}
		c.Set(userContextKey, user)
		c.Next()
	}
}

func currentUser(c *gin.Context) model.User { return c.MustGet(userContextKey).(model.User) }

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

func permissionMask(value int64) rbac.Permission { return rbac.Permission(uint64(value)) }
func databaseMask(value rbac.Permission) int64   { return int64(uint64(value)) }
