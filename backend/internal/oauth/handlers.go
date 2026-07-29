package oauth

import (
	"crypto/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/appdeps"
	"github.com/newtspeak/newt-server/backend/internal/model"
	"github.com/newtspeak/newt-server/backend/internal/security"
	"gorm.io/gorm/clause"
)

const (
	deviceCodeTTL   = 15 * time.Minute
	defaultInterval = 5
	userCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // 去掉易混 0/O/1/I
)

type handler struct {
	deps   appdeps.Deps
	tokens *security.TokenManager
}

func newHandler(deps appdeps.Deps) *handler {
	return &handler{
		deps:   deps,
		tokens: security.NewTokenManager(deps.Cfg.JWTSecret, deps.Cfg.AccessTokenTTL),
	}
}

type errorBody struct {
	Error apiError `json:"error"`
}
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, errorBody{apiError{code, message}})
}

func bindJSON(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return false
	}
	return true
}

// --- Device Code ---

type deviceCodeRequest struct {
	ClientID string `json:"client_id" binding:"required"`
	Scope    string `json:"scope"`
}

type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// postDeviceCode POST /oauth/v1/device/code
func (h *handler) postDeviceCode(c *gin.Context) {
	var input deviceCodeRequest
	if !bindJSON(c, &input) {
		return
	}
	client, ok := lookupClient(input.ClientID)
	if !ok {
		fail(c, http.StatusBadRequest, "INVALID_CLIENT", "未知的 client_id")
		return
	}
	scopeList := ParseScopeList(input.Scope)
	normalized, ok := ValidateRequestedScopes(scopeList)
	if !ok {
		fail(c, http.StatusBadRequest, "INVALID_SCOPE", "包含未知的 scope")
		return
	}
	devicePlain, deviceHash, err := security.NewRefreshToken() // 复用 32 字节随机 + hash
	if err != nil {
		fail(c, http.StatusInternalServerError, "INTERNAL", "生成 device_code 失败")
		return
	}
	userCode, err := generateUserCode()
	if err != nil {
		fail(c, http.StatusInternalServerError, "INTERNAL", "生成 user_code 失败")
		return
	}
	now := time.Now().UTC()
	row := model.OAuthDeviceCode{
		ID:             uuid.New(),
		DeviceCodeHash: deviceHash,
		UserCode:       userCode,
		ClientID:       client.ID,
		Scope:          normalized,
		Status:         model.OAuthDevicePending,
		Interval:       defaultInterval,
		ExpiresAt:      now.Add(deviceCodeTTL),
		CreatedAt:      now,
	}
	if err := h.deps.DB.Create(&row).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存 device code 失败")
		return
	}
	verURI, verComplete := verificationURIs(c, userCode)
	c.JSON(http.StatusOK, deviceCodeResponse{
		DeviceCode:              devicePlain,
		UserCode:                userCode,
		VerificationURI:         verURI,
		VerificationURIComplete: verComplete,
		ExpiresIn:               int(deviceCodeTTL.Seconds()),
		Interval:                defaultInterval,
	})
}

func verificationURIs(c *gin.Context, userCode string) (uri, complete string) {
	// 优先 PUBLIC_CLIENT_ORIGIN（用户 Web / Desktop 打包页根），否则用请求 Host 拼绝对路径。
	base := strings.TrimRight(os.Getenv("PUBLIC_CLIENT_ORIGIN"), "/")
	if base == "" {
		scheme := "https"
		if c.Request.TLS == nil {
			// 反代常见：X-Forwarded-Proto
			if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
				scheme = proto
			} else {
				scheme = "http"
			}
		}
		host := c.Request.Host
		if host == "" {
			host = "localhost"
		}
		base = scheme + "://" + host
	}
	uri = base + "/oauth/device"
	complete = uri + "?user_code=" + userCode
	return uri, complete
}

func generateUserCode() (string, error) {
	// 格式 XXXX-XXXX
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	chars := make([]byte, 8)
	alpha := []byte(userCodeAlphabet)
	for i := 0; i < 8; i++ {
		chars[i] = alpha[int(buf[i])%len(alpha)]
	}
	return string(chars[0:4]) + "-" + string(chars[4:8]), nil
}

// getDeviceInfo GET /oauth/v1/device/:user_code — 授权页预览（不泄露 device_code）。
func (h *handler) getDeviceInfo(c *gin.Context) {
	code := normalizeUserCode(c.Param("user_code"))
	var row model.OAuthDeviceCode
	if err := h.deps.DB.Where("user_code = ?", code).First(&row).Error; err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "授权码不存在")
		return
	}
	if time.Now().UTC().After(row.ExpiresAt) && row.Status == model.OAuthDevicePending {
		_ = h.deps.DB.Model(&row).Update("status", model.OAuthDeviceExpired)
		row.Status = model.OAuthDeviceExpired
	}
	client, _ := lookupClient(row.ClientID)
	c.JSON(http.StatusOK, gin.H{
		"user_code":    row.UserCode,
		"client_id":    row.ClientID,
		"client_name":  client.Name,
		"description":  client.Description,
		"scope":        row.Scope,
		"status":       row.Status,
		"expires_at":   row.ExpiresAt,
		"expires_in":   int(time.Until(row.ExpiresAt).Seconds()),
	})
}

type deviceDecisionRequest struct {
	UserCode string `json:"user_code" binding:"required"`
	// Scope 可选：用户勾选后的子集；空则授予请求的全部（再经 system_admin 过滤）。
	Scope string `json:"scope"`
}

// postDeviceApprove POST /oauth/v1/device/approve — 需 aud=client 用户会话。
func (h *handler) postDeviceApprove(c *gin.Context) {
	user, ok := h.requireClientUser(c)
	if !ok {
		return
	}
	var input deviceDecisionRequest
	if !bindJSON(c, &input) {
		return
	}
	code := normalizeUserCode(input.UserCode)
	var row model.OAuthDeviceCode
	err := h.deps.DB.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_code = ? AND status = ?", code, model.OAuthDevicePending).
		First(&row).Error
	if err != nil {
		fail(c, http.StatusNotFound, "NOT_FOUND", "授权码不存在或已处理")
		return
	}
	if time.Now().UTC().After(row.ExpiresAt) {
		_ = h.deps.DB.Model(&row).Update("status", model.OAuthDeviceExpired)
		fail(c, http.StatusGone, "EXPIRED", "授权码已过期")
		return
	}
	granted := row.Scope
	if strings.TrimSpace(input.Scope) != "" {
		// 仅允许请求 scope 的子集
		reqSet := map[string]struct{}{}
		for _, s := range ParseScopeList(row.Scope) {
			reqSet[s] = struct{}{}
		}
		var subset []string
		for _, s := range ParseScopeList(input.Scope) {
			if _, ok := reqSet[s]; !ok {
				fail(c, http.StatusBadRequest, "INVALID_SCOPE", "不能授予未请求的 scope: "+s)
				return
			}
			subset = append(subset, s)
		}
		granted = security.NormalizeScopes(subset)
	}
	granted = FilterScopesForUser(granted, user.SystemAdmin)
	if granted == "" {
		fail(c, http.StatusBadRequest, "INVALID_SCOPE", "没有可授予的权限")
		return
	}
	now := time.Now().UTC()
	uid := user.ID
	if err := h.deps.DB.Model(&row).Updates(map[string]any{
		"status":        model.OAuthDeviceApproved,
		"user_id":       uid,
		"granted_scope": granted,
		"approved_at":   now,
	}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "保存授权失败")
		return
	}
	client, _ := lookupClient(row.ClientID)
	c.JSON(http.StatusOK, gin.H{
		"status":         model.OAuthDeviceApproved,
		"client_id":      row.ClientID,
		"client_name":    client.Name,
		"granted_scope":  granted,
		"user_id":        user.ID,
		"username":       user.Username,
	})
}

// postDeviceDeny POST /oauth/v1/device/deny
func (h *handler) postDeviceDeny(c *gin.Context) {
	if _, ok := h.requireClientUser(c); !ok {
		return
	}
	var input deviceDecisionRequest
	if !bindJSON(c, &input) {
		return
	}
	code := normalizeUserCode(input.UserCode)
	res := h.deps.DB.Model(&model.OAuthDeviceCode{}).
		Where("user_code = ? AND status = ?", code, model.OAuthDevicePending).
		Update("status", model.OAuthDeviceDenied)
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "DATABASE_ERROR", "拒绝授权失败")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "NOT_FOUND", "授权码不存在或已处理")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": model.OAuthDeviceDenied})
}

func normalizeUserCode(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

// requireClientUser 用 aud=client access token 识别当前桌面/Web 用户。
func (h *handler) requireClientUser(c *gin.Context) (model.User, bool) {
	header := c.GetHeader("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "缺少访问令牌")
		return model.User{}, false
	}
	userID, err := h.tokens.ParseAccessTokenWithAudience(strings.TrimPrefix(header, "Bearer "), security.AudienceClient)
	if err != nil {
		fail(c, http.StatusUnauthorized, "UNAUTHORIZED", err.Error())
		return model.User{}, false
	}
	var user model.User
	if err := h.deps.DB.First(&user, "id = ?", userID).Error; err != nil {
		fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "用户不存在")
		return model.User{}, false
	}
	if user.Disabled() {
		fail(c, http.StatusForbidden, "ACCOUNT_DISABLED", "账号已被平台禁用")
		return model.User{}, false
	}
	return user, true
}

// --- Token endpoint ---

type tokenRequest struct {
	GrantType    string `json:"grant_type" form:"grant_type" binding:"required"`
	DeviceCode   string `json:"device_code" form:"device_code"`
	ClientID     string `json:"client_id" form:"client_id"`
	RefreshToken string `json:"refresh_token" form:"refresh_token"`
	Scope        string `json:"scope" form:"scope"`
	// Authorization Code + PKCE
	Code         string `json:"code" form:"code"`
	CodeVerifier string `json:"code_verifier" form:"code_verifier"`
	RedirectURI  string `json:"redirect_uri" form:"redirect_uri"`
}

type tokenResponse struct {
	AccessToken      string    `json:"access_token"`
	TokenType        string    `json:"token_type"`
	ExpiresIn        int       `json:"expires_in"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	Scope            string    `json:"scope"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitempty"`
}

// postToken POST /oauth/v1/token
// 支持 JSON 与 form-urlencoded（RFC 兼容）。
func (h *handler) postToken(c *gin.Context) {
	var input tokenRequest
	ct := c.ContentType()
	if strings.Contains(ct, "application/x-www-form-urlencoded") {
		if err := c.ShouldBind(&input); err != nil {
			oauthTokenError(c, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	} else {
		if err := c.ShouldBindJSON(&input); err != nil {
			oauthTokenError(c, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	switch input.GrantType {
	case "urn:ietf:params:oauth:grant-type:device_code":
		h.tokenDeviceCode(c, input)
	case "authorization_code":
		h.tokenAuthorizationCode(c, input)
	case "refresh_token":
		h.tokenRefresh(c, input)
	default:
		oauthTokenError(c, http.StatusBadRequest, "unsupported_grant_type", "不支持的 grant_type")
	}
}

func oauthTokenError(c *gin.Context, status int, errCode, desc string) {
	// RFC 风格错误字段（CLI 友好）+ 与项目一致的 error 包装可选
	c.JSON(status, gin.H{
		"error":             errCode,
		"error_description": desc,
	})
}

func (h *handler) tokenDeviceCode(c *gin.Context, input tokenRequest) {
	if input.DeviceCode == "" || input.ClientID == "" {
		oauthTokenError(c, http.StatusBadRequest, "invalid_request", "device_code 与 client_id 必填")
		return
	}
	if _, ok := lookupClient(input.ClientID); !ok {
		oauthTokenError(c, http.StatusBadRequest, "invalid_client", "未知 client_id")
		return
	}
	var row model.OAuthDeviceCode
	err := h.deps.DB.Where("device_code_hash = ? AND client_id = ?", security.HashToken(input.DeviceCode), input.ClientID).First(&row).Error
	if err != nil {
		oauthTokenError(c, http.StatusBadRequest, "invalid_grant", "device_code 无效")
		return
	}
	now := time.Now().UTC()
	if now.After(row.ExpiresAt) {
		_ = h.deps.DB.Model(&row).Update("status", model.OAuthDeviceExpired)
		oauthTokenError(c, http.StatusBadRequest, "expired_token", "device_code 已过期")
		return
	}
	// 轮询节流
	if row.LastPollAt != nil {
		minWait := time.Duration(row.Interval) * time.Second
		if now.Sub(*row.LastPollAt) < minWait {
			oauthTokenError(c, http.StatusBadRequest, "slow_down", "轮询过快")
			_ = h.deps.DB.Model(&row).Updates(map[string]any{
				"interval":     row.Interval + 5,
				"last_poll_at": now,
			})
			return
		}
	}
	_ = h.deps.DB.Model(&row).Update("last_poll_at", now)

	switch row.Status {
	case model.OAuthDevicePending:
		oauthTokenError(c, http.StatusBadRequest, "authorization_pending", "用户尚未完成授权")
		return
	case model.OAuthDeviceDenied:
		oauthTokenError(c, http.StatusBadRequest, "access_denied", "用户拒绝了授权")
		return
	case model.OAuthDeviceConsumed, model.OAuthDeviceExpired:
		oauthTokenError(c, http.StatusBadRequest, "invalid_grant", "device_code 已失效")
		return
	case model.OAuthDeviceApproved:
		// continue
	default:
		oauthTokenError(c, http.StatusBadRequest, "invalid_grant", "device_code 状态异常")
		return
	}
	if row.UserID == nil {
		oauthTokenError(c, http.StatusBadRequest, "invalid_grant", "授权记录不完整")
		return
	}
	var user model.User
	if err := h.deps.DB.First(&user, "id = ?", *row.UserID).Error; err != nil || user.Disabled() {
		oauthTokenError(c, http.StatusBadRequest, "invalid_grant", "用户不可用")
		return
	}
	// 一次性消费
	res := h.deps.DB.Model(&model.OAuthDeviceCode{}).
		Where("id = ? AND status = ?", row.ID, model.OAuthDeviceApproved).
		Update("status", model.OAuthDeviceConsumed)
	if res.Error != nil || res.RowsAffected == 0 {
		oauthTokenError(c, http.StatusBadRequest, "invalid_grant", "device_code 已被使用")
		return
	}
	h.issueAgentTokens(c, user, row.ClientID, row.GrantedScope, true)
}

func (h *handler) tokenRefresh(c *gin.Context, input tokenRequest) {
	if input.RefreshToken == "" {
		oauthTokenError(c, http.StatusBadRequest, "invalid_request", "refresh_token 必填")
		return
	}
	var stored model.RefreshToken
	err := h.deps.DB.Where(
		"token_hash = ? AND audience = ? AND revoked_at IS NULL AND expires_at > ?",
		security.HashToken(input.RefreshToken), security.AudienceAgent, time.Now().UTC(),
	).First(&stored).Error
	if err != nil {
		oauthTokenError(c, http.StatusBadRequest, "invalid_grant", "refresh_token 无效或已过期")
		return
	}
	if input.ClientID != "" && stored.ClientID != "" && input.ClientID != stored.ClientID {
		oauthTokenError(c, http.StatusBadRequest, "invalid_client", "client_id 与 refresh 不匹配")
		return
	}
	now := time.Now().UTC()
	if err := h.deps.DB.Model(&stored).Update("revoked_at", now).Error; err != nil {
		oauthTokenError(c, http.StatusInternalServerError, "server_error", "轮换失败")
		return
	}
	var user model.User
	if err := h.deps.DB.First(&user, "id = ?", stored.UserID).Error; err != nil || user.Disabled() {
		oauthTokenError(c, http.StatusBadRequest, "invalid_grant", "用户不可用")
		return
	}
	scope := stored.Scope
	// refresh 不可扩大 scope；可缩小
	if strings.TrimSpace(input.Scope) != "" {
		reqSet := map[string]struct{}{}
		for _, s := range ParseScopeList(stored.Scope) {
			reqSet[s] = struct{}{}
		}
		var subset []string
		for _, s := range ParseScopeList(input.Scope) {
			if _, ok := reqSet[s]; ok {
				subset = append(subset, s)
			}
		}
		scope = security.NormalizeScopes(subset)
	}
	scope = FilterScopesForUser(scope, user.SystemAdmin)
	h.issueAgentTokensForSession(c, user, stored.ClientID, scope, stored.SessionID, stored.SessionCreatedAt, true)
}

func (h *handler) issueAgentTokens(c *gin.Context, user model.User, clientID, scope string, withRefresh bool) {
	h.issueAgentTokensForSession(c, user, clientID, scope, uuid.New(), time.Now().UTC(), withRefresh)
}

func (h *handler) issueAgentTokensForSession(c *gin.Context, user model.User, clientID, scope string, sessionID uuid.UUID, sessionCreatedAt time.Time, withRefresh bool) {
	access, accessExpiry, err := h.tokens.AccessTokenWithClaims(user.ID, security.AudienceAgent, sessionID.String(), scope, clientID)
	if err != nil {
		oauthTokenError(c, http.StatusInternalServerError, "server_error", "签发 access_token 失败")
		return
	}
	resp := tokenResponse{
		AccessToken:     access,
		TokenType:       "Bearer",
		ExpiresIn:       int(time.Until(accessExpiry).Seconds()),
		Scope:           scope,
		AccessExpiresAt: accessExpiry,
	}
	wantRefresh := withRefresh && security.ScopeContains(scope, ScopeOfflineAccess)
	if wantRefresh {
		refresh, refreshHash, err := security.NewRefreshToken()
		if err != nil {
			oauthTokenError(c, http.StatusInternalServerError, "server_error", "签发 refresh_token 失败")
			return
		}
		refreshExpiry := time.Now().UTC().Add(h.deps.Cfg.RefreshTokenTTL)
		device, platform := security.DeviceInfo(c.GetHeader("User-Agent"))
		stored := model.RefreshToken{
			ID: uuid.New(), UserID: user.ID, TokenHash: refreshHash,
			Audience: security.AudienceAgent, ClientID: clientID, Scope: scope,
			SessionID: sessionID, SessionCreatedAt: sessionCreatedAt, ExpiresAt: refreshExpiry,
			DeviceName: device, Platform: platform, IPAddress: c.ClientIP(),
		}
		if err := h.deps.DB.Create(&stored).Error; err != nil {
			oauthTokenError(c, http.StatusInternalServerError, "server_error", "保存 refresh_token 失败")
			return
		}
		resp.RefreshToken = refresh
		resp.RefreshExpiresAt = refreshExpiry
	}
	c.JSON(http.StatusOK, resp)
}

// --- Revoke ---

type revokeRequest struct {
	Token string `json:"token" binding:"required"`
	// TokenTypeHint "access_token" | "refresh_token"（可选）
	TokenTypeHint string `json:"token_type_hint"`
}

// postRevoke POST /oauth/v1/revoke — 幂等；主要吊销 refresh。
func (h *handler) postRevoke(c *gin.Context) {
	var input revokeRequest
	if !bindJSON(c, &input) {
		return
	}
	now := time.Now().UTC()
	h.deps.DB.Model(&model.RefreshToken{}).
		Where("token_hash = ? AND audience = ? AND revoked_at IS NULL", security.HashToken(input.Token), security.AudienceAgent).
		Update("revoked_at", now)
	// access 为无状态 JWT，无法服务端吊销；依赖短 TTL。
	c.Status(http.StatusNoContent)
}

// --- Metadata / userinfo ---

func (h *handler) metadata(c *gin.Context) {
	base := publicAPIBase(c)
	c.JSON(http.StatusOK, gin.H{
		"issuer":                 base,
		"device_authorization_endpoint": base + "/oauth/v1/device/code",
		"token_endpoint":         base + "/oauth/v1/token",
		"revocation_endpoint":    base + "/oauth/v1/revoke",
		"userinfo_endpoint":      base + "/oauth/v1/userinfo",
		"grant_types_supported": []string{
			"urn:ietf:params:oauth:grant-type:device_code",
			"authorization_code",
			"refresh_token",
		},
		"authorization_endpoint": base + "/oauth/authorize", // 由 Desktop/Web 承载 UI
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported": []string{
			ScopeOpenID, ScopeProfile, ScopeOfflineAccess,
			ScopeGapiFull, ScopeGapiRead, ScopeGapiGuildsManage,
			ScopePlatformRead, ScopePlatformAdmin,
		},
		"response_types_supported": []string{},
		"code_challenge_methods_supported": []string{"S256"},
	})
}

func publicAPIBase(c *gin.Context) string {
	if v := strings.TrimRight(os.Getenv("PUBLIC_API_BASE"), "/"); v != "" {
		return v
	}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	return scheme + "://" + c.Request.Host
}

// userinfo GET /oauth/v1/userinfo — 需 aud=agent。
func (h *handler) userinfo(c *gin.Context) {
	header := c.GetHeader("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "缺少访问令牌")
		return
	}
	parsed, err := h.tokens.ParseAccess(strings.TrimPrefix(header, "Bearer "))
	if err != nil || parsed.Audience != security.AudienceAgent {
		fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "无效或已过期的访问令牌")
		return
	}
	var user model.User
	if err := h.deps.DB.First(&user, "id = ?", parsed.UserID).Error; err != nil || user.Disabled() {
		fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "用户不可用")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"sub":           user.ID,
		"username":      user.Username,
		"display_name":  user.DisplayName,
		"email":         user.Email,
		"system_admin":  user.SystemAdmin,
		"client_id":     parsed.ClientID,
		"scope":         parsed.Scope,
	})
}


