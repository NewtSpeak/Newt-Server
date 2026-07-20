package botapi

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/config"
	"github.com/owlspeak/owl-server/backend/internal/eventbus"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"github.com/owlspeak/owl-server/backend/internal/security"
	"gorm.io/gorm"
)

// tokenPrefix bot token 明文前缀：便于泄露扫描与人工辨识（同类如 ghp_ / xoxb-）。
const tokenPrefix = "owlbot_"

// tokenDisplayPrefixLen 存库供后台展示的明文前缀长度（owlbot_ + 8 位）。
const tokenDisplayPrefixLen = len(tokenPrefix) + 8

// lastUsedThrottle last_used_at 落库节流：一分钟内最多写一次，避免高频 API 打爆该列。
const lastUsedThrottle = time.Minute

// service bot 模块运行时依赖：后台管理平面与 bot 开放平面共用一个实例
//（包级单例，见 ensureService），token 解析结果无状态、限流按 bot 计。
type service struct {
	db  *gorm.DB
	bus *eventbus.Bus
	cfg config.Config
	// limiter bot 开放 API 限流：每 bot 20 QPS、突发 40（防失控 bot 打爆平台）。
	limiter *botLimiter
	// lastUsedMu/lastUsed last_used_at 节流表（tokenID → 上次落库时间）。
	lastUsedMu sync.Mutex
	lastUsed   map[uuid.UUID]time.Time
}

// sharedService 包级单例：Register（后台平面）与 RegisterBotAPI（开放平面）
// 在 server 装配阶段的单 goroutine 内先后调用，共用同一实例。
var sharedService *service

func ensureService(db *gorm.DB, bus *eventbus.Bus, cfg config.Config) *service {
	if sharedService == nil {
		sharedService = &service{
			db:       db,
			bus:      bus,
			cfg:      cfg,
			limiter:  newBotLimiter(20, 40),
			lastUsed: make(map[uuid.UUID]time.Time),
		}
	}
	return sharedService
}

// ---------- 统一错误输出（对齐仓库 {"error":{"code","message"}} 约定） ----------

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

func notFound(c *gin.Context) {
	fail(c, http.StatusNotFound, "NOT_FOUND", "资源不存在或不可见")
}

func bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return false
	}
	return true
}

// ---------- bot token 生成与校验 ----------

// newBotToken 生成 bot 长期令牌：明文 owlbot_<base64url 32B>，仅创建响应返回一次；
// DB 只存 SHA-256（同 RefreshToken 策略）。
func newBotToken() (plain, hash, displayPrefix string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	plain = tokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return plain, security.HashToken(plain), plain[:tokenDisplayPrefixLen], nil
}

var errTokenInvalid = errors.New("bot token 无效或已吊销")

// resolveToken 校验 bot token 明文：查 hash → 检查吊销/过期 → 加载 Bot 与其 User。
func (s *service) resolveToken(raw string) (model.Bot, model.User, model.BotToken, error) {
	var bot model.Bot
	var user model.User
	var token model.BotToken
	if !strings.HasPrefix(raw, tokenPrefix) {
		return bot, user, token, errTokenInvalid
	}
	err := s.db.First(&token, "token_hash = ? AND revoked_at IS NULL", security.HashToken(raw)).Error
	if err != nil {
		return bot, user, token, errTokenInvalid
	}
	if token.ExpiresAt != nil && token.ExpiresAt.Before(time.Now().UTC()) {
		return bot, user, token, errTokenInvalid
	}
	if err := s.db.First(&bot, "id = ?", token.BotID).Error; err != nil {
		return bot, user, token, errTokenInvalid
	}
	if err := s.db.First(&user, "id = ?", bot.UserID).Error; err != nil {
		return bot, user, token, errTokenInvalid
	}
	s.touchToken(token.ID)
	return bot, user, token, nil
}

// touchToken 节流更新 last_used_at（异步尽力，失败不影响主路径）。
func (s *service) touchToken(tokenID uuid.UUID) {
	now := time.Now().UTC()
	s.lastUsedMu.Lock()
	last, ok := s.lastUsed[tokenID]
	if ok && now.Sub(last) < lastUsedThrottle {
		s.lastUsedMu.Unlock()
		return
	}
	s.lastUsed[tokenID] = now
	s.lastUsedMu.Unlock()
	go func() {
		_ = s.db.Model(&model.BotToken{}).Where("id = ?", tokenID).Update("last_used_at", now).Error
	}()
}

// ---------- bot 开放平面认证中间件 ----------

const (
	botContextKey     = "botapi.current_bot"
	botUserContextKey = "botapi.current_bot_user"
)

// requireBotAuth bot 开放平面认证：接受 `Authorization: Bot <token>`（规范形式）
// 与 `Authorization: Bearer <token>`（SDK 兼容形式），校验通过后注入 bot 与其用户。
func (s *service) requireBotAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		raw := ""
		switch {
		case strings.HasPrefix(header, "Bot "):
			raw = strings.TrimPrefix(header, "Bot ")
		case strings.HasPrefix(header, "Bearer "):
			raw = strings.TrimPrefix(header, "Bearer ")
		}
		if raw == "" {
			fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "缺少 bot token（Authorization: Bot <token>）")
			c.Abort()
			return
		}
		bot, user, _, err := s.resolveToken(strings.TrimSpace(raw))
		if err != nil {
			fail(c, http.StatusUnauthorized, "UNAUTHORIZED", err.Error())
			c.Abort()
			return
		}
		if !s.limiter.Allow(bot.ID) {
			fail(c, http.StatusTooManyRequests, "RATE_LIMITED", "bot API 请求过于频繁")
			c.Abort()
			return
		}
		c.Set(botContextKey, bot)
		c.Set(botUserContextKey, user)
		c.Next()
	}
}

// currentBot 取当前认证 bot（requireBotAuth 之后调用）。
func currentBot(c *gin.Context) model.Bot { return c.MustGet(botContextKey).(model.Bot) }

// CurrentBotUser 取当前认证 bot 的用户身份，注入 Deps.CurrentUser 供
// message/voice 等领域模块以「bot 即用户」的方式复用全部权限与业务逻辑。
func CurrentBotUser(c *gin.Context) model.User {
	return c.MustGet(botUserContextKey).(model.User)
}

// authenticateGatewayToken bot Gateway 的 IDENTIFY 认证：token 为 bot token，
// 返回 bot 用户与其已安装（加入）的 guild 列表。
func (s *service) authenticateGatewayToken(token string) (model.User, []uuid.UUID, error) {
	_, user, _, err := s.resolveToken(strings.TrimSpace(token))
	if err != nil {
		return model.User{}, nil, err
	}
	var guildIDs []uuid.UUID
	if err := s.db.Model(&model.Member{}).Where("user_id = ?", user.ID).Pluck("guild_id", &guildIDs).Error; err != nil {
		return model.User{}, nil, err
	}
	return user, guildIDs, nil
}

// ---------- 按 bot 限流（令牌桶） ----------

type botBucket struct {
	tokens   float64
	lastFill time.Time
}

type botLimiter struct {
	mu      sync.Mutex
	buckets map[uuid.UUID]*botBucket
	rate    float64
	burst   float64
}

func newBotLimiter(rate, burst float64) *botLimiter {
	return &botLimiter{buckets: make(map[uuid.UUID]*botBucket), rate: rate, burst: burst}
}

func (l *botLimiter) Allow(botID uuid.UUID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	bucket, ok := l.buckets[botID]
	if !ok {
		bucket = &botBucket{tokens: l.burst, lastFill: now}
		l.buckets[botID] = bucket
	}
	elapsed := now.Sub(bucket.lastFill).Seconds()
	if elapsed > 0 {
		bucket.tokens += elapsed * l.rate
		if bucket.tokens > l.burst {
			bucket.tokens = l.burst
		}
		bucket.lastFill = now
	}
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}
