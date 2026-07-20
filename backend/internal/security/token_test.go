package security

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func newTestManager(t *testing.T) *TokenManager {
	t.Helper()
	return NewTokenManager(testSecret, time.Minute)
}

// TestAudienceRoundTrip 各受众签发后按同受众解析成功且 subject 一致。
func TestAudienceRoundTrip(t *testing.T) {
	m := newTestManager(t)
	userID := uuid.New()
	for _, audience := range []string{AudienceAdmin, AudienceClient} {
		raw, _, err := m.AccessTokenWithAudience(userID, audience)
		if err != nil {
			t.Fatalf("签发 %s token 失败: %v", audience, err)
		}
		parsed, err := m.ParseAccessTokenWithAudience(raw, audience)
		if err != nil {
			t.Fatalf("解析 %s token 失败: %v", audience, err)
		}
		if parsed != userID {
			t.Fatalf("subject = %s，期待 %s", parsed, userID)
		}
	}
}

// TestAudienceCrossRejected 交叉受众一律拒绝：client token 不可当 admin 用，反之亦然。
func TestAudienceCrossRejected(t *testing.T) {
	m := newTestManager(t)
	userID := uuid.New()

	clientToken, _, err := m.AccessTokenWithAudience(userID, AudienceClient)
	if err != nil {
		t.Fatalf("签发 client token 失败: %v", err)
	}
	if _, err := m.ParseAccessToken(clientToken); err == nil {
		t.Fatalf("client token 通过了 admin 受众校验，凭证隔离失效")
	}

	adminToken, _, err := m.AccessToken(userID)
	if err != nil {
		t.Fatalf("签发 admin token 失败: %v", err)
	}
	if _, err := m.ParseAccessTokenWithAudience(adminToken, AudienceClient); err == nil {
		t.Fatalf("admin token 通过了 client 受众校验，凭证隔离失效")
	}
}

// TestLegacyTokenWithoutAudience 过渡策略：aud 上线前签发的旧 token（无 aud claim）
// 视为 admin——admin 受众放行、client 受众拒绝。
func TestLegacyTokenWithoutAudience(t *testing.T) {
	m := newTestManager(t)
	userID := uuid.New()
	now := time.Now().UTC()
	// 按旧版 AccessToken 的 claims 结构手工签发（无 Audience 字段）。
	legacy := Claims{RegisteredClaims: jwt.RegisteredClaims{
		Subject: userID.String(), Issuer: "owl-server", ID: uuid.NewString(),
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
	}}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, legacy).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("签发旧格式 token 失败: %v", err)
	}
	parsed, err := m.ParseAccessToken(raw)
	if err != nil {
		t.Fatalf("旧 token 应按 admin 受众放行，实际: %v", err)
	}
	if parsed != userID {
		t.Fatalf("subject = %s，期待 %s", parsed, userID)
	}
	if _, err := m.ParseAccessTokenWithAudience(raw, AudienceClient); err == nil {
		t.Fatalf("旧 token（视为 admin）不应通过 client 受众校验")
	}
}

// TestExpiredTokenRejected 过期 token 任何受众均拒绝。
func TestExpiredTokenRejected(t *testing.T) {
	m := NewTokenManager(testSecret, -time.Minute)
	raw, _, err := m.AccessTokenWithAudience(uuid.New(), AudienceClient)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if _, err := m.ParseAccessTokenWithAudience(raw, AudienceClient); err == nil {
		t.Fatalf("过期 token 不应通过校验")
	}
}

// TestWrongSecretRejected 换密钥后旧 token 失效（签名校验）。
func TestWrongSecretRejected(t *testing.T) {
	m := newTestManager(t)
	raw, _, err := m.AccessTokenWithAudience(uuid.New(), AudienceClient)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	other := NewTokenManager("another-secret-another-secret-32", time.Minute)
	if _, err := other.ParseAccessTokenWithAudience(raw, AudienceClient); err == nil {
		t.Fatalf("不同密钥签发的 token 不应通过校验")
	}
}
