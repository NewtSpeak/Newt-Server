// cascadetoken.go 级联凭证签发（docs 08 §6.2 E.4 / docs 15 BG.2）：
// 复用 Media Token 的 Ed25519 密钥（kid 同源，SFU 经 Enroll/RegisterAck 已持有验签公钥），
// claims 绑定 logical_room_id + epoch + 边两端 node_id，短 TTL（分钟级）。
// child 建边时在 hello 帧出示；parent 侧做三重校验之一（池内 + 证书 + token）。
package mediatoken

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// CascadeTokenTyp typ claim 固定值，防与 Media Token 混用。
const CascadeTokenTyp = "cascade"

// CascadeClaims 级联 token claims。
type CascadeClaims struct {
	V      int    `json:"v"`
	Typ    string `json:"typ"` // 恒为 "cascade"
	RID    string `json:"rid"` // logical_room_id
	Epoch  uint64 `json:"epoch"`
	Parent string `json:"parent"` // parent_node_id
	Child  string `json:"child"`  // child_node_id
	IAT    int64  `json:"iat"`
	EXP    int64  `json:"exp"`
	JTI    string `json:"jti"`
}

func (c CascadeClaims) GetExpirationTime() (*jwt.NumericDate, error) {
	return jwt.NewNumericDate(time.Unix(c.EXP, 0)), nil
}
func (c CascadeClaims) GetIssuedAt() (*jwt.NumericDate, error) {
	return jwt.NewNumericDate(time.Unix(c.IAT, 0)), nil
}
func (c CascadeClaims) GetNotBefore() (*jwt.NumericDate, error) { return nil, nil }
func (c CascadeClaims) GetIssuer() (string, error)              { return "", nil }
func (c CascadeClaims) GetSubject() (string, error)             { return "", nil }
func (c CascadeClaims) GetAudience() (jwt.ClaimStrings, error)  { return nil, nil }

// SignCascade 签发一条边的级联 token（EdDSA，header 带 kid）。
func (m *Manager) SignCascade(roomID string, epoch uint64, parentNodeID, childNodeID string, ttl time.Duration) (string, error) {
	active := m.keys[len(m.keys)-1]
	now := time.Now().UTC()
	claims := CascadeClaims{
		V: 1, Typ: CascadeTokenTyp,
		RID: roomID, Epoch: epoch, Parent: parentNodeID, Child: childNodeID,
		IAT: now.Unix(), EXP: now.Add(ttl).Unix(), JTI: uuid.NewString(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = active.kid
	return token.SignedString(active.priv)
}
