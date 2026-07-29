package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/eventbus"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/platformbadge"
	"github.com/newtspeak/newt-server/backend/internal/rbac"
	"github.com/newtspeak/newt-server/backend/internal/security"
	"github.com/newtspeak/newt-server/backend/internal/sfudeploy"
	"gorm.io/gorm"
)

const userContextKey = "authenticated_user"

var errRegistrationClosed = errors.New("系统初始化注册已关闭")

type API struct {
	db              *gorm.DB
	tokens          *security.TokenManager
	loginLimiter    *security.LoginLimiter
	refreshTokenTTL time.Duration
	// sfu SFU 配套子系统依赖（节点注册表 + Media Token 签发），经 AttachSFU 注入。
	sfu *SFUOptions
	// sfuDeploy SFU 节点一键部署编排器，经 AttachSfuDeploy 注入；为 nil 时相关路由 503。
	sfuDeploy *sfudeploy.Manager
	// bus 事件总线（经 AttachEventBus 注入；为 nil 时静默不发布，纯单测场景兼容）。
	bus *eventbus.Bus
}

// AttachEventBus 注入事件总线，使 RBAC/频道等变更端点能发布 Gateway 事件（docs 14 §3.2）。
func (a *API) AttachEventBus(bus *eventbus.Bus) { a.bus = bus }

// publish 发布事件（bus 未注入时为 no-op）。
func (a *API) publish(event eventbus.Event) {
	if a.bus != nil {
		a.bus.Publish(event)
	}
}

func New(db *gorm.DB, tokens *security.TokenManager, refreshTokenTTL time.Duration) *API {
	return &API{db: db, tokens: tokens, loginLimiter: security.NewLoginLimiter(5, 20, 15*time.Minute), refreshTokenTTL: refreshTokenTTL}
}

func (a *API) RegisterRoutes(group *gin.RouterGroup) {
	auth := group.Group("/auth")
	auth.POST("/register", a.register)
	auth.GET("/registration-status", a.registrationStatus)
	auth.POST("/login", a.login)
	auth.POST("/refresh", a.refresh)
	auth.POST("/logout", a.logout)
	auth.GET("/me", a.requireAuth(), a.me)

	protected := group.Group("")
	protected.Use(a.requireAuth())
	protected.POST("/guilds", a.createGuild)
	protected.GET("/guilds", a.listGuilds)
	protected.GET("/guilds/:guildID/channels", a.listChannels)
	protected.GET("/guilds/:guildID/members", a.listMembers)
	// 角色/频道创建/权限覆盖/服务器生命周期等结构管理端点已抽出为共享包
	// internal/guildapi，由 server.New 以模块形式挂载到双认证平面
	//（后台 /api/v1 保持原路径不变，用户端 /gapi/v1 走标准 RBAC 无 SystemAdmin 短路）。

	// SFU 节点管理（基于 sfucontrol gRPC 控制面，handler 见 sfu_admin.go）：
	// 创建占位 + enrollment token、状态迁移、吊销；Newt-SFU 节点只讲 proto/owlsfu/v1 的
	// gRPC 协议（Enroll + ControlService.Channel，默认外连 :9443），Media Token 验签
	// 公钥经 Enroll/RegisterAck 下发。
	admin := protected.Group("/admin/sfu", a.requireSystemAdmin())
	admin.POST("/nodes", a.createSfuNode)
	admin.GET("/nodes", a.listSfuNodes)
	admin.GET("/topology", a.listSfuTopology)
	admin.PATCH("/nodes/:nodeID", a.updateSfuNode)
	admin.DELETE("/nodes/:nodeID", a.deleteSfuNode)
	admin.POST("/nodes/:nodeID/revoke", a.revokeSfuNode)
	admin.POST("/nodes/:nodeID/update-binary", a.updateSfuBinary)
	admin.GET("/releases", a.listSfuReleases)

	// SFU 节点一键自动部署（handler 见 sfu_deploy.go，编排在 internal/sfudeploy）：
	// SSH 登录目标机 → 装依赖 → 拉二进制 → 创建占位并签 token → 写 env/systemd →
	// 启动 → 等待节点自行 enroll 上线。进度经 SFU_DEPLOYMENT_UPDATE 定向推给发起人。
	admin.GET("/deploy-preflight", a.getSfuDeployPreflight)
	admin.GET("/deploy-servers", a.listSfuDeployServers)
	admin.POST("/deploy-servers", a.createSfuDeployServer)
	admin.DELETE("/deploy-servers/:serverID", a.deleteSfuDeployServer)
	admin.POST("/deployments", a.createSfuDeployment)
	admin.GET("/deployments", a.listSfuDeployments)
	admin.GET("/deployments/:deploymentID", a.getSfuDeployment)
	admin.POST("/deployments/:deploymentID/cancel", a.cancelSfuDeployment)

	// 语音会话收敛说明（端到端统一专项）：早期本文件平行实现的
	// POST /guilds/:gid/channels/:cid/voice/join|leave、/guilds/:gid/voice/refresh-token、
	// /guilds/:gid/voice/disconnect、PATCH /guilds/:gid/voice/participants/:uid/caps
	// 已整体移除（原 handler 文件 voice.go 已删除）。唯一进房主路径为 internal/voice 模块的
	// POST /voice/join（含调度、级联、caps 全链路与 VOICE_* 事件），管理员踢人为
	// POST /guilds/:guildID/voice/disconnect（voice.Register 挂载）。
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
	user := model.User{ID: uuid.New(), Username: input.Username, Email: input.Email, PasswordHash: hash, SystemAdmin: true}
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "owl:first-user-registration").Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.User{}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errRegistrationClosed
		}
		return tx.Create(&user).Error
	})
	if err != nil {
		if errors.Is(err, errRegistrationClosed) {
			fail(c, http.StatusForbidden, "REGISTRATION_CLOSED", "系统初始化已完成，后台仅允许登录")
			return
		}
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			fail(c, http.StatusConflict, "ACCOUNT_EXISTS", "用户名或邮箱已被使用")
			return
		}
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "创建账号失败")
		return
	}
	a.issueTokens(c, http.StatusCreated, user)
}

func (a *API) registrationStatus(c *gin.Context) {
	var count int64
	if err := a.db.Model(&model.User{}).Count(&count).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "读取初始化状态失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"registration_open": count == 0})
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
	if allowed, retryAfter := a.loginLimiter.Allow(c.ClientIP(), identifier); !allowed {
		seconds := int(retryAfter.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		c.Header("Retry-After", strconv.Itoa(seconds))
		fail(c, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "登录尝试过多，请稍后再试")
		return
	}
	var user model.User
	// 后台登录仅对系统管理员开放；非系统管与密码错误返回同一错误（且同样计入限流），
	// 不暴露「账号存在但无后台权限」的信息。普通用户请走用户端登录。
	if err := a.db.Where("LOWER(email) = ? OR LOWER(username) = ?", identifier, identifier).First(&user).Error; err != nil || !security.VerifyPassword(user.PasswordHash, input.Password) || !user.SystemAdmin {
		a.loginLimiter.Failure(c.ClientIP(), identifier)
		fail(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "账号或密码错误")
		return
	}
	// 平台禁用拦截（platformadmin）：凭证正确但账号被禁用，明确告知。
	if user.Disabled() {
		fail(c, http.StatusForbidden, "ACCOUNT_DISABLED", "账号已被平台禁用")
		return
	}
	a.loginLimiter.Success(identifier)
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
	// 仅接受后台受众（admin）的 refresh token，用户端 refresh token 不可换取后台凭证。
	err := a.db.Where("token_hash = ? AND audience = ? AND revoked_at IS NULL AND expires_at > ?", security.HashToken(input.Token), security.AudienceAdmin, time.Now().UTC()).First(&stored).Error
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
	// 轮换时复查系统管理员身份与禁用状态：被撤职/被平台禁用的账号
	// 无法凭旧 refresh token 续期后台凭证。
	if err := a.db.First(&user, "id = ?", stored.UserID).Error; err != nil || !user.SystemAdmin || user.Disabled() {
		fail(c, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "刷新令牌无效")
		return
	}
	// 轮换后的新 token 继承会话链（session_id / 会话创建时间），
	// 使会话列表与「改密码保留当前会话」能跨轮换识别同一次登录。
	a.issueTokensForSession(c, http.StatusOK, user, stored.SessionID, stored.SessionCreatedAt)
}

func (a *API) logout(c *gin.Context) {
	var input refreshRequest
	if !bind(c, &input) {
		return
	}
	now := time.Now().UTC()
	// 只吊销后台受众的令牌，与 refresh 的受众过滤保持一致。
	a.db.Model(&model.RefreshToken{}).Where("token_hash = ? AND audience = ? AND revoked_at IS NULL", security.HashToken(input.Token), security.AudienceAdmin).Update("revoked_at", now)
	c.Status(http.StatusNoContent)
}

func (a *API) me(c *gin.Context) {
	c.JSON(http.StatusOK, platformbadge.ViewOf(currentUser(c)))
}

type tokenResponse struct {
	AccessToken      string                 `json:"access_token"`
	RefreshToken     string                 `json:"refresh_token"`
	AccessExpiresAt  time.Time              `json:"access_expires_at"`
	RefreshExpiresAt time.Time              `json:"refresh_expires_at"`
	User             platformbadge.UserView `json:"user"`
}

// issueTokens 登录/注册路径：开启新的会话链。
func (a *API) issueTokens(c *gin.Context, status int, user model.User) {
	a.issueTokensForSession(c, status, user, uuid.New(), time.Now().UTC())
}

// issueTokensForSession 按指定会话链签发 access/refresh token 对（refresh 轮换时继承旧链）。
func (a *API) issueTokensForSession(c *gin.Context, status int, user model.User, sessionID uuid.UUID, sessionCreatedAt time.Time) {
	access, accessExpiry, err := a.tokens.AccessTokenForSession(user.ID, security.AudienceAdmin, sessionID.String())
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
	// 会话设备元数据（docs 01 FR-27）：登录/轮换请求即来自该设备，按当前请求采集。
	device, platform := security.DeviceInfo(c.GetHeader("User-Agent"))
	stored := model.RefreshToken{
		ID: uuid.New(), UserID: user.ID, TokenHash: refreshHash, Audience: security.AudienceAdmin,
		SessionID: sessionID, SessionCreatedAt: sessionCreatedAt, ExpiresAt: refreshExpiry,
		DeviceName: device, Platform: platform, IPAddress: c.ClientIP(),
	}
	if err := a.db.Create(&stored).Error; err != nil {
		fail(c, 500, "DATABASE_ERROR", "保存刷新令牌失败")
		return
	}
	c.JSON(status, tokenResponse{
		AccessToken: access, RefreshToken: refresh,
		AccessExpiresAt: accessExpiry, RefreshExpiresAt: refreshExpiry,
		User: platformbadge.ViewOf(user),
	})
}

func (a *API) requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			fail(c, 401, "UNAUTHORIZED", "缺少访问令牌")
			c.Abort()
			return
		}
		raw := strings.TrimPrefix(header, "Bearer ")
		parsed, err := a.tokens.ParseAccess(raw)
		if err != nil {
			fail(c, 401, "UNAUTHORIZED", err.Error())
			c.Abort()
			return
		}
		switch parsed.Audience {
		case security.AudienceAdmin:
			// 管理台会话
		case security.AudienceAgent:
			// OAuth agent：需 platform.* scope；写操作需 platform.admin
			if !security.ScopeHasAny(parsed.Scope, "platform.read", "platform.admin") {
				fail(c, 403, "INSUFFICIENT_SCOPE", "缺少平台访问权限")
				c.Abort()
				return
			}
			if !security.ScopeContains(parsed.Scope, "platform.admin") {
				switch c.Request.Method {
				case http.MethodGet, http.MethodHead, http.MethodOptions:
				default:
					fail(c, 403, "INSUFFICIENT_SCOPE", "当前授权为平台只读")
					c.Abort()
					return
				}
			}
		default:
			// 用户端（aud=client）token 打后台 API 一律 401。
			fail(c, 401, "UNAUTHORIZED", "无效或已过期的访问令牌")
			c.Abort()
			return
		}
		var user model.User
		if err := a.db.First(&user, "id = ?", parsed.UserID).Error; err != nil {
			fail(c, 401, "UNAUTHORIZED", "用户不存在")
			c.Abort()
			return
		}
		if user.Disabled() {
			fail(c, 401, "ACCOUNT_DISABLED", "账号已被平台禁用")
			c.Abort()
			return
		}
		// agent 访问平台 API 必须 dual-check system_admin（不轻信 token 内 flag）
		if parsed.Audience == security.AudienceAgent && !user.SystemAdmin {
			fail(c, 403, "FORBIDDEN", "需要系统管理员权限")
			c.Abort()
			return
		}
		c.Set(userContextKey, user)
		c.Set("oauth_client_id", parsed.ClientID)
		c.Set("oauth_scope", parsed.Scope)
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

func databaseMask(value rbac.Permission) int64 { return int64(uint64(value)) }
