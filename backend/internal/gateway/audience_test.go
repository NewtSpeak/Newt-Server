package gateway

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/security"
)

// TestDbAuthenticatorRejectsCrossAudience 用户端 Gateway（aud=client）拒绝后台 token，
// 反之亦然。db 传 nil：交叉受众 token 在 JWT 解析阶段即被拒绝，不触达数据库。
func TestDbAuthenticatorRejectsCrossAudience(t *testing.T) {
	tokens := security.NewTokenManager("0123456789abcdef0123456789abcdef", time.Minute)

	adminToken, _, err := tokens.AccessToken(uuid.New())
	if err != nil {
		t.Fatalf("签发 admin token 失败: %v", err)
	}
	clientToken, _, err := tokens.AccessTokenWithAudience(uuid.New(), security.AudienceClient)
	if err != nil {
		t.Fatalf("签发 client token 失败: %v", err)
	}

	clientGateway := &dbAuthenticator{tokens: tokens, audience: security.AudienceClient}
	if _, _, err := clientGateway.Authenticate(adminToken); err == nil {
		t.Fatalf("admin token 通过了用户端 Gateway 认证，凭证隔离失效")
	}

	adminGateway := &dbAuthenticator{tokens: tokens, audience: security.AudienceAdmin}
	if _, _, err := adminGateway.Authenticate(clientToken); err == nil {
		t.Fatalf("client token 通过了后台 Gateway 认证，凭证隔离失效")
	}
}
