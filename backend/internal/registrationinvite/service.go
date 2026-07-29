package registrationinvite

import (
	"crypto/rand"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"gorm.io/gorm"
)

// 邀请码字母表：与 guild 邀请（moderation）同风格，去掉易混淆字符（0O1lI）。
const inviteAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
const inviteCodeLength = 10

// newInviteCode 生成加密随机短码。
func newInviteCode() (string, error) {
	code := make([]byte, inviteCodeLength)
	max := big.NewInt(int64(len(inviteAlphabet)))
	for i := range code {
		index, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		code[i] = inviteAlphabet[index.Int64()]
	}
	return string(code), nil
}

// Status 注册邀请的解析结果，公开预检与 signup 消费共用同一判定。
type Status int

const (
	// StatusActive 邀请有效可用。
	StatusActive Status = iota
	// StatusNotFound 邀请不存在（对外 404，不泄露细节）。
	StatusNotFound
	// StatusExpired 邀请已过期（对外 410）。
	StatusExpired
	// StatusExhausted 邀请次数已用尽（对外 410）。
	StatusExhausted
	// StatusRevoked 邀请已被管理员撤销（对外 410）。
	StatusRevoked
)

// statusLabel 列表/返回体中的状态字段取值。
func statusLabel(invite model.RegistrationInvite, now time.Time) string {
	switch {
	case invite.RevokedAt != nil:
		return "revoked"
	case invite.ExpiresAt != nil && !invite.ExpiresAt.After(now):
		return "expired"
	case invite.MaxUses > 0 && invite.Uses >= invite.MaxUses:
		return "exhausted"
	default:
		return "active"
	}
}

// Resolve 按短码解析注册邀请并校验有效性。
func Resolve(db *gorm.DB, code string) (model.RegistrationInvite, Status) {
	var invite model.RegistrationInvite
	code = strings.TrimSpace(code)
	if code == "" {
		return invite, StatusNotFound
	}
	if err := db.First(&invite, "code = ?", code).Error; err != nil {
		return invite, StatusNotFound
	}
	switch statusLabel(invite, time.Now().UTC()) {
	case "revoked":
		return invite, StatusRevoked
	case "expired":
		return invite, StatusExpired
	case "exhausted":
		return invite, StatusExhausted
	}
	return invite, StatusActive
}

// ConsumeUse 原子消耗一次注册邀请使用次数（与新用户创建同事务调用，防并发超用）。
// 并发用尽或已撤销时返回 false。
func ConsumeUse(tx *gorm.DB, inviteID uuid.UUID) (bool, error) {
	result := tx.Model(&model.RegistrationInvite{}).
		Where("id = ? AND revoked_at IS NULL AND (max_uses = 0 OR uses < max_uses)", inviteID).
		Update("uses", gorm.Expr("uses + 1"))
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// baseURL 对外根地址：优先配置 PUBLIC_BASE_URL，否则按请求推导（支持反代头）。
func (h *api) baseURL(c *gin.Context) string {
	if h.deps.Cfg.PublicBaseURL != "" {
		return h.deps.Cfg.PublicBaseURL
	}
	scheme := "http"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}

// loadPortal 读取全局门户配置（publicinvite 管理，单行，缺省值兜底）：
// 深链协议与产品名（公开预检的 server_name）均取自本表。
func (h *api) loadPortal() model.InvitePortalConfig {
	portal := model.InvitePortalConfig{ID: 1, AppName: "NewtSpeak", DeepLinkScheme: "newtspeak"}
	_ = h.deps.DB.First(&portal, "id = 1").Error
	if portal.AppName == "" {
		portal.AppName = "NewtSpeak"
	}
	if portal.DeepLinkScheme == "" {
		portal.DeepLinkScheme = "newtspeak"
	}
	return portal
}

// deepLink 客户端注册深链：携带后端地址与邀请码，客户端免手工填写连接信息。
func deepLink(scheme, base, code string) string {
	return scheme + "://register?server=" + url.QueryEscape(base) + "&code=" + url.QueryEscape(code)
}
