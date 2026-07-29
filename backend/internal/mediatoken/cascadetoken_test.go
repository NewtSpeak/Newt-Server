package mediatoken

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/newtspeak/newt-server/backend/internal/secretstore"
)

// TestSignCascade 验证级联 token 签发：EdDSA 可验签、claims 绑定 room+epoch+edge、TTL 生效。
func TestSignCascade(t *testing.T) {
	mgr, err := Load(secretstore.NewMemoryStore(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := mgr.SignCascade("room-1", 7, "node-p", "node-c", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	pub := mgr.PublicKeys()
	if len(pub) != 1 {
		t.Fatalf("应恰有 1 把公钥，got %d", len(pub))
	}
	claims := &CascadeClaims{}
	parsed, err := jwt.NewParser(jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithExpirationRequired()).
		ParseWithClaims(raw, claims, func(tok *jwt.Token) (any, error) {
			kid, _ := tok.Header["kid"].(string)
			if kid != pub[0].Kid {
				t.Fatalf("kid 不匹配: %s != %s", kid, pub[0].Kid)
			}
			return ed25519.PublicKey(pub[0].Key), nil
		})
	if err != nil || !parsed.Valid {
		t.Fatalf("验签失败: %v", err)
	}
	if claims.Typ != CascadeTokenTyp || claims.RID != "room-1" || claims.Epoch != 7 ||
		claims.Parent != "node-p" || claims.Child != "node-c" {
		t.Fatalf("claims 绑定不完整: %+v", claims)
	}
	if claims.EXP-claims.IAT != 120 {
		t.Fatalf("TTL 应为 120s, got %d", claims.EXP-claims.IAT)
	}

	// 过期 token 必须验签失败（短 TTL 语义）。
	expired, err := mgr.SignCascade("room-1", 7, "node-p", "node-c", -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = jwt.NewParser(jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithExpirationRequired()).
		ParseWithClaims(expired, &CascadeClaims{}, func(*jwt.Token) (any, error) {
			return ed25519.PublicKey(pub[0].Key), nil
		})
	if err == nil {
		t.Fatal("过期级联 token 应验签失败")
	}
}
