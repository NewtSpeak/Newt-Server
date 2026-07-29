package mediatoken

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/newtspeak/newt-server/backend/internal/secretstore"
)

func keyForKid(t *testing.T, manager *Manager, kid string) ed25519.PublicKey {
	t.Helper()
	for _, key := range manager.PublicKeys() {
		if key.Kid == kid {
			return key.Key
		}
	}
	t.Fatalf("找不到 kid=%s 的公钥", kid)
	return nil
}

func parseToken(t *testing.T, manager *Manager, token string) (*jwt.Token, jwt.MapClaims) {
	t.Helper()
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodEdDSA {
			return nil, errors.New("签名算法必须是 EdDSA")
		}
		kid, _ := token.Header["kid"].(string)
		return keyForKid(t, manager, kid), nil
	})
	if err != nil {
		t.Fatalf("验签失败: %v", err)
	}
	return parsed, claims
}

func TestSignAndVerifyClaims(t *testing.T) {
	manager, err := Load(secretstore.NewMemoryStore(), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input := Claims{
		UID: "user-1", GID: "guild-1", CID: "channel-1",
		NID: "node-1", RID: "room-1", SID: "session-1",
		Caps: []string{"join", "subscribe_audio", "publish_audio"},
	}
	token, expiresAt, err := manager.Sign(input)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	parsed, claims := parseToken(t, manager, token)
	if !parsed.Valid {
		t.Fatal("token 应有效")
	}
	if kid, _ := parsed.Header["kid"].(string); len(kid) != 8 {
		t.Fatalf("kid 应为 8 位 hex，实际 %q", kid)
	}
	for field, want := range map[string]string{
		"uid": "user-1", "gid": "guild-1", "cid": "channel-1",
		"nid": "node-1", "rid": "room-1", "sid": "session-1",
	} {
		if got, _ := claims[field].(string); got != want {
			t.Fatalf("claim %s = %v，期望 %s", field, claims[field], want)
		}
	}
	if v, _ := claims["v"].(float64); v != 1 {
		t.Fatalf("claim v 应为 1，实际 %v", claims["v"])
	}
	caps, _ := claims["caps"].([]any)
	if len(caps) != 3 {
		t.Fatalf("caps 应有 3 项，实际 %v", claims["caps"])
	}
	if jti, _ := claims["jti"].(string); jti == "" {
		t.Fatal("jti 不能为空")
	}
	exp, _ := claims["exp"].(float64)
	if int64(exp) != expiresAt.Unix() {
		t.Fatalf("exp=%d 与返回的 expiresAt=%d 不一致", int64(exp), expiresAt.Unix())
	}
	if remaining := time.Until(expiresAt); remaining <= 4*time.Minute || remaining > 5*time.Minute {
		t.Fatalf("TTL 应约 5 分钟，实际 %s", remaining)
	}
}

func TestExpiredTokenRejected(t *testing.T) {
	manager, err := Load(secretstore.NewMemoryStore(), -1*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := manager.Sign(Claims{UID: "u", NID: "n", RID: "r", SID: "s", Caps: []string{"join"}})
	if err != nil {
		t.Fatal(err)
	}
	_, parseErr := jwt.ParseWithClaims(token, jwt.MapClaims{}, func(token *jwt.Token) (any, error) {
		kid, _ := token.Header["kid"].(string)
		return keyForKid(t, manager, kid), nil
	})
	if !errors.Is(parseErr, jwt.ErrTokenExpired) {
		t.Fatalf("过期 token 应被拒绝，实际错误: %v", parseErr)
	}
}

func TestKeysPersistAcrossRestart(t *testing.T) {
	store := secretstore.NewMemoryStore()
	first, err := Load(store, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(store, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	firstKeys, secondKeys := first.PublicKeys(), second.PublicKeys()
	if len(firstKeys) != 1 || len(secondKeys) != 1 {
		t.Fatalf("应各有 1 把密钥: %d/%d", len(firstKeys), len(secondKeys))
	}
	if firstKeys[0].Kid != secondKeys[0].Kid || !firstKeys[0].Key.Equal(secondKeys[0].Key) {
		t.Fatal("重启后密钥应保持一致")
	}
	if len(firstKeys[0].Key) != ed25519.PublicKeySize {
		t.Fatalf("公钥应为 32 字节，实际 %d", len(firstKeys[0].Key))
	}
}
