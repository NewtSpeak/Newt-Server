package security

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// 令牌受众（JWT aud claim）：各认证平面凭证互不相通。
//   - AudienceAdmin：后台管理台（/api/v1），仅系统管理员登录可获得；
//   - AudienceClient：用户端（/gapi/v1），开放注册的普通用户凭证；
//   - AudienceAgent：CLI / AI Agent（OAuth2 用户委托），带 scope，经 /oauth/v1 签发。
const (
	AudienceAdmin  = "admin"
	AudienceClient = "client"
	AudienceAgent  = "agent"
)

type Claims struct {
	jwt.RegisteredClaims
	// SessionID 登录会话链 ID（对应 RefreshToken.SessionID）：标记该 access token
	// 属于哪次登录，供「当前会话」识别（改密码保留当前会话 / 会话列表标记）。
	// 旧 token 无此 claim，为空字符串。
	SessionID string `json:"sid,omitempty"`
	// Scope OAuth2 空格分隔权限范围（仅 aud=agent 使用；client/admin 为空）。
	Scope string `json:"scope,omitempty"`
	// ClientID OAuth 客户端标识（仅 aud=agent，如 owl-cli）。
	ClientID string `json:"client_id,omitempty"`
}

type TokenManager struct {
	secret    []byte
	accessTTL time.Duration
}

func NewTokenManager(secret string, accessTTL time.Duration) *TokenManager {
	return &TokenManager{secret: []byte(secret), accessTTL: accessTTL}
}

// AccessToken 签发后台（aud=admin）access token。
// 保留旧签名以兼容既有调用方；用户端请使用 AccessTokenWithAudience。
func (m *TokenManager) AccessToken(userID uuid.UUID) (string, time.Time, error) {
	return m.AccessTokenWithAudience(userID, AudienceAdmin)
}

// AccessTokenWithAudience 签发指定受众的 access token（aud claim 单值，无会话标记）。
func (m *TokenManager) AccessTokenWithAudience(userID uuid.UUID, audience string) (string, time.Time, error) {
	return m.AccessTokenForSession(userID, audience, "")
}

// AccessTokenForSession 签发带会话链标记（sid claim）的 access token；
// sessionID 为空时不写入 sid（兼容不关心会话的调用方）。
func (m *TokenManager) AccessTokenForSession(userID uuid.UUID, audience, sessionID string) (string, time.Time, error) {
	return m.AccessTokenWithClaims(userID, audience, sessionID, "", "")
}

// AccessTokenWithClaims 签发带会话、scope、client_id 的 access token（OAuth agent 用）。
func (m *TokenManager) AccessTokenWithClaims(userID uuid.UUID, audience, sessionID, scope, clientID string) (string, time.Time, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(m.accessTTL)
	claims := Claims{RegisteredClaims: jwt.RegisteredClaims{
		Subject: userID.String(), Issuer: "owl-server", ID: uuid.NewString(),
		Audience: jwt.ClaimStrings{audience},
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expiresAt),
	}, SessionID: sessionID, Scope: scope, ClientID: clientID}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	return token, expiresAt, err
}

// ParsedAccess 解析后的 access token 摘要（含 audience / scope，供中间件做平面与权限判定）。
type ParsedAccess struct {
	UserID    uuid.UUID
	Audience  string
	SessionID string
	Scope     string
	ClientID  string
}

// ParseAccess 校验签名/有效期/签发者并返回 claims 摘要；不强制受众（由调用方校验）。
func (m *TokenManager) ParseAccess(raw string) (ParsedAccess, error) {
	token, err := jwt.ParseWithClaims(raw, &Claims{}, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("无效签名算法")
		}
		return m.secret, nil
	}, jwt.WithIssuer("owl-server"), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return ParsedAccess{}, errors.New("无效或已过期的访问令牌")
	}
	claims, ok := token.Claims.(*Claims)
	if !ok {
		return ParsedAccess{}, errors.New("无效访问令牌")
	}
	audience := AudienceAdmin // 无 aud 的旧 token 视为 admin
	if len(claims.Audience) > 0 {
		audience = claims.Audience[0]
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return ParsedAccess{}, errors.New("无效访问令牌")
	}
	return ParsedAccess{
		UserID: userID, Audience: audience, SessionID: claims.SessionID,
		Scope: claims.Scope, ClientID: claims.ClientID,
	}, nil
}

// ScopeContains 判断空格分隔的 scope 串是否包含目标 scope。
func ScopeContains(granted, need string) bool {
	if need == "" {
		return true
	}
	for _, part := range splitScopes(granted) {
		if part == need {
			return true
		}
	}
	return false
}

// ScopeHasAny 判断 granted 是否包含 needs 中任一 scope。
func ScopeHasAny(granted string, needs ...string) bool {
	for _, need := range needs {
		if ScopeContains(granted, need) {
			return true
		}
	}
	return false
}

func splitScopes(scope string) []string {
	var out []string
	start := -1
	for i := 0; i <= len(scope); i++ {
		if i == len(scope) || scope[i] == ' ' {
			if start >= 0 {
				out = append(out, scope[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	return out
}

// TokenSessionID 提取 access token 中的会话链 ID（sid claim）。
// 仅校验签名/有效期/签发者，不校验受众——调用方必须已在认证中间件中完成受众校验；
// 旧 token 或解析失败返回空字符串（调用方按「无会话标记」降级处理）。
func (m *TokenManager) TokenSessionID(raw string) string {
	token, err := jwt.ParseWithClaims(raw, &Claims{}, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("无效签名算法")
		}
		return m.secret, nil
	}, jwt.WithIssuer("owl-server"), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return ""
	}
	claims, ok := token.Claims.(*Claims)
	if !ok {
		return ""
	}
	return claims.SessionID
}

// ParseAccessToken 校验后台（aud=admin）access token。
// 保留旧签名以兼容既有调用方；用户端请使用 ParseAccessTokenWithAudience。
func (m *TokenManager) ParseAccessToken(raw string) (uuid.UUID, error) {
	return m.ParseAccessTokenWithAudience(raw, AudienceAdmin)
}

// ParseAccessTokenWithAudience 解析并校验 access token，且要求受众匹配 audience。
// 过渡策略：audience 功能上线前签发的旧 token 不带 aud claim，一律视为 admin
//（旧 token 只可能由后台登录签发），因此仅在期望受众为 admin 时放行；
// 待旧 token 全部自然过期（AccessTokenTTL 很短）后该兼容分支即无实际流量。
func (m *TokenManager) ParseAccessTokenWithAudience(raw, audience string) (uuid.UUID, error) {
	token, err := jwt.ParseWithClaims(raw, &Claims{}, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("无效签名算法")
		}
		return m.secret, nil
	}, jwt.WithIssuer("owl-server"), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return uuid.Nil, errors.New("无效或已过期的访问令牌")
	}
	claims, ok := token.Claims.(*Claims)
	if !ok {
		return uuid.Nil, errors.New("无效访问令牌")
	}
	actual := AudienceAdmin // 无 aud 的旧 token 视为 admin（见函数注释的过渡策略）
	if len(claims.Audience) > 0 {
		actual = claims.Audience[0]
	}
	if actual != audience {
		// 统一返回与其他失败相同的措辞，避免向调用方暴露「另一受众存在」的信息。
		return uuid.Nil, errors.New("无效或已过期的访问令牌")
	}
	return uuid.Parse(claims.Subject)
}

// NormalizeScopes 去重、排序稳定化 OAuth scope 列表（空格分隔输出）。
func NormalizeScopes(scopes []string) string {
	seen := map[string]struct{}{}
	var ordered []string
	for _, raw := range scopes {
		for _, s := range splitScopes(raw) {
			if s == "" {
				continue
			}
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			ordered = append(ordered, s)
		}
	}
	if len(ordered) == 0 {
		return ""
	}
	out := ordered[0]
	for i := 1; i < len(ordered); i++ {
		out += " " + ordered[i]
	}
	return out
}

func NewRefreshToken() (plain, hash string, err error) {
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		return "", "", err
	}
	plain = base64.RawURLEncoding.EncodeToString(value)
	return plain, HashToken(plain), nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
