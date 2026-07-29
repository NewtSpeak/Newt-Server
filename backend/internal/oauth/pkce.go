package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/security"
)

const authCodeTTL = 5 * time.Minute

// 允许的 redirect_uri：仅 loopback 与 newtspeak 深链（public client）。
func allowedRedirectURI(uri string) bool {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		host := strings.ToLower(u.Hostname())
		return host == "127.0.0.1" || host == "localhost" || host == "[::1]"
	case "newtspeak":
		return true
	default:
		return false
	}
}

func verifyS256(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	// RFC 7636：BASE64URL-ENCODE(SHA256(verifier)) without padding
	encoded := base64.RawURLEncoding.EncodeToString(sum[:])
	return encoded == challenge
}

type authorizeApproveRequest struct {
	ClientID            string `json:"client_id" binding:"required"`
	RedirectURI         string `json:"redirect_uri" binding:"required"`
	Scope               string `json:"scope"`
	CodeChallenge       string `json:"code_challenge" binding:"required"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	State               string `json:"state"`
}

// postAuthorizeApprove POST /oauth/v1/authorize/approve
// Desktop/Web 已登录用户同意后签发一次性 authorization code。
func (h *handler) postAuthorizeApprove(c *gin.Context) {
	user, ok := h.requireClientUser(c)
	if !ok {
		return
	}
	var input authorizeApproveRequest
	if !bindJSON(c, &input) {
		return
	}
	if _, ok := lookupClient(input.ClientID); !ok {
		fail(c, http.StatusBadRequest, "INVALID_CLIENT", "未知的 client_id")
		return
	}
	if !allowedRedirectURI(input.RedirectURI) {
		fail(c, http.StatusBadRequest, "INVALID_REDIRECT", "redirect_uri 仅允许 loopback 或 newtspeak://")
		return
	}
	method := strings.ToUpper(strings.TrimSpace(input.CodeChallengeMethod))
	if method == "" {
		method = "S256"
	}
	if method != "S256" {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "仅支持 code_challenge_method=S256")
		return
	}
	if len(input.CodeChallenge) < 43 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "code_challenge 无效")
		return
	}
	scopeList := ParseScopeList(input.Scope)
	normalized, ok := ValidateRequestedScopes(scopeList)
	if !ok {
		fail(c, http.StatusBadRequest, "INVALID_SCOPE", "包含未知的 scope")
		return
	}
	granted := FilterScopesForUser(normalized, user.SystemAdmin)
	if granted == "" {
		fail(c, http.StatusBadRequest, "INVALID_SCOPE", "没有可授予的权限")
		return
	}
	plain, hash, err := security.NewRefreshToken()
	if err != nil {
		fail(c, http.StatusInternalServerError, "INTERNAL", "生成授权码失败")
		return
	}
	now := time.Now().UTC()
	row := model.OAuthAuthCode{
		ID: uuid.New(), CodeHash: hash, ClientID: input.ClientID,
		UserID: user.ID, RedirectURI: input.RedirectURI, Scope: granted,
		CodeChallenge: input.CodeChallenge, CodeChallengeMethod: method,
		ExpiresAt: now.Add(authCodeTTL), CreatedAt: now,
	}
	if err := h.deps.DB.Create(&row).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存授权码失败")
		return
	}
	// 组装 redirect（供前端 location 或 CLI loopback）
	redir, _ := url.Parse(input.RedirectURI)
	q := redir.Query()
	q.Set("code", plain)
	if input.State != "" {
		q.Set("state", input.State)
	}
	redir.RawQuery = q.Encode()
	c.JSON(http.StatusOK, gin.H{
		"code":          plain,
		"expires_in":    int(authCodeTTL.Seconds()),
		"redirect_uri":  redir.String(),
		"scope":         granted,
		"state":         input.State,
		"client_id":     input.ClientID,
	})
}

func (h *handler) tokenAuthorizationCode(c *gin.Context, input tokenRequest) {
	if input.Code == "" || input.ClientID == "" || input.CodeVerifier == "" || input.RedirectURI == "" {
		oauthTokenError(c, http.StatusBadRequest, "invalid_request", "code / client_id / code_verifier / redirect_uri 必填")
		return
	}
	if _, ok := lookupClient(input.ClientID); !ok {
		oauthTokenError(c, http.StatusBadRequest, "invalid_client", "未知 client_id")
		return
	}
	var row model.OAuthAuthCode
	err := h.deps.DB.Where("code_hash = ? AND client_id = ?", security.HashToken(input.Code), input.ClientID).First(&row).Error
	if err != nil {
		oauthTokenError(c, http.StatusBadRequest, "invalid_grant", "authorization code 无效")
		return
	}
	now := time.Now().UTC()
	if row.UsedAt != nil || now.After(row.ExpiresAt) {
		oauthTokenError(c, http.StatusBadRequest, "invalid_grant", "authorization code 已失效")
		return
	}
	if row.RedirectURI != input.RedirectURI {
		oauthTokenError(c, http.StatusBadRequest, "invalid_grant", "redirect_uri 不匹配")
		return
	}
	if !verifyS256(input.CodeVerifier, row.CodeChallenge) {
		oauthTokenError(c, http.StatusBadRequest, "invalid_grant", "code_verifier 校验失败")
		return
	}
	// 一次性
	res := h.deps.DB.Model(&model.OAuthAuthCode{}).
		Where("id = ? AND used_at IS NULL", row.ID).
		Update("used_at", now)
	if res.Error != nil || res.RowsAffected == 0 {
		oauthTokenError(c, http.StatusBadRequest, "invalid_grant", "authorization code 已被使用")
		return
	}
	var user model.User
	if err := h.deps.DB.First(&user, "id = ?", row.UserID).Error; err != nil || user.Disabled() {
		oauthTokenError(c, http.StatusBadRequest, "invalid_grant", "用户不可用")
		return
	}
	scope := FilterScopesForUser(row.Scope, user.SystemAdmin)
	h.issueAgentTokens(c, user, row.ClientID, scope, true)
}
