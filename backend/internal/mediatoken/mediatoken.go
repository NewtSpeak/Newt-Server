// Package mediatoken Media Token 签发（docs 协议 §1）：Ed25519 (EdDSA) JWT，
// header 带 kid，密钥列表持久化于 ClusterSecret 以支持将来轮换。
package mediatoken

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/secretstore"
)

const secretName = "media_token_keys"

// Claims Media Token 声明（docs 协议 §1，字段名严格对齐）。
// iat/exp/jti 由 Sign 填充。
type Claims struct {
	V    int      `json:"v"`
	UID  string   `json:"uid"`
	GID  string   `json:"gid"`
	CID  string   `json:"cid"`
	NID  string   `json:"nid"`
	RID  string   `json:"rid"`
	SID  string   `json:"sid"`
	Caps []string `json:"caps"`
	// Bot 机器人会话标记（bot 专项）：SFU 据此在参与者/信令中携带 is_bot，
	// 让音频流中的机器人拥有独立的用户标记。
	Bot bool `json:"bot,omitempty"`
	// Hidden 系统管理员隐身临场标记（adminpresence 专项）：SFU 抑制该会话的
	// participant_joined/left 广播并将其从 ready 快照剔除，实现语音隐身。
	Hidden bool `json:"hidden,omitempty"`
	// Audit 音频审计标记（adminpresence 专项）：SFU 据此录制该会话的上行音频
	// 并上传到主节点服务器。
	Audit bool  `json:"audit,omitempty"`
	IAT   int64 `json:"iat"`
	EXP   int64 `json:"exp"`
	JTI   string `json:"jti"`
}

// Valid 实现 jwt.Claims（golang-jwt v5 通过 MapClaims/RegisteredClaims 校验；
// 自定义结构自行提供过期校验）。
func (c Claims) GetExpirationTime() (*jwt.NumericDate, error) {
	return jwt.NewNumericDate(time.Unix(c.EXP, 0)), nil
}
func (c Claims) GetIssuedAt() (*jwt.NumericDate, error) {
	return jwt.NewNumericDate(time.Unix(c.IAT, 0)), nil
}
func (c Claims) GetNotBefore() (*jwt.NumericDate, error) { return nil, nil }
func (c Claims) GetIssuer() (string, error)              { return "", nil }
func (c Claims) GetSubject() (string, error)             { return "", nil }
func (c Claims) GetAudience() (jwt.ClaimStrings, error)  { return nil, nil }

// PublicKey 下发给 SFU 的验签公钥（Enroll 响应 / RegisterAck）。
type PublicKey struct {
	Kid string
	Key ed25519.PublicKey // 32 字节原始公钥
}

type storedKey struct {
	Kid        string `json:"kid"`
	PrivateKey string `json:"private_key"` // base64(ed25519 私钥 seed+pub，64 字节)
	CreatedAt  int64  `json:"created_at"`
}

// Manager Media Token 签发器。当前用密钥列表的最后一把签发，全部公钥参与下发。
type Manager struct {
	keys []struct {
		kid  string
		priv ed25519.PrivateKey
	}
	ttl time.Duration
}

// Load 从 Store 加载签名密钥；不存在时生成 Ed25519 密钥对（kid=8 位随机 hex）并持久化。
func Load(store secretstore.Store, ttl time.Duration) (*Manager, error) {
	raw, ok, err := store.Get(secretName)
	if err != nil {
		return nil, fmt.Errorf("读取 Media Token 密钥: %w", err)
	}
	var stored []storedKey
	if ok {
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			return nil, fmt.Errorf("解析 Media Token 密钥 JSON: %w", err)
		}
	}
	if len(stored) == 0 {
		kidBytes := make([]byte, 4)
		if _, err := rand.Read(kidBytes); err != nil {
			return nil, fmt.Errorf("生成 kid: %w", err)
		}
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("生成 Ed25519 密钥: %w", err)
		}
		stored = []storedKey{{
			Kid:        hex.EncodeToString(kidBytes),
			PrivateKey: base64.StdEncoding.EncodeToString(priv),
			CreatedAt:  time.Now().Unix(),
		}}
		serialized, err := json.Marshal(stored)
		if err != nil {
			return nil, err
		}
		if err := store.Set(secretName, string(serialized)); err != nil {
			return nil, fmt.Errorf("持久化 Media Token 密钥: %w", err)
		}
	}
	manager := &Manager{ttl: ttl}
	for _, key := range stored {
		privBytes, err := base64.StdEncoding.DecodeString(key.PrivateKey)
		if err != nil || len(privBytes) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("Media Token 私钥（kid=%s）格式无效", key.Kid)
		}
		manager.keys = append(manager.keys, struct {
			kid  string
			priv ed25519.PrivateKey
		}{key.Kid, ed25519.PrivateKey(privBytes)})
	}
	return manager, nil
}

// TTL 当前配置的 token 有效期。
func (m *Manager) TTL() time.Duration { return m.ttl }

// Sign 签发 Media Token：填充 v/iat/exp/jti，EdDSA 签名，header 带当前 kid。
func (m *Manager) Sign(claims Claims) (token string, expiresAt time.Time, err error) {
	active := m.keys[len(m.keys)-1]
	now := time.Now().UTC()
	expiresAt = now.Add(m.ttl)
	claims.V = 1
	claims.IAT = now.Unix()
	claims.EXP = expiresAt.Unix()
	claims.JTI = uuid.NewString()
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	jwtToken.Header["kid"] = active.kid
	token, err = jwtToken.SignedString(active.priv)
	return token, expiresAt, err
}

// PublicKeys 全部验签公钥（按 kid）。
func (m *Manager) PublicKeys() []PublicKey {
	result := make([]PublicKey, 0, len(m.keys))
	for _, key := range m.keys {
		result = append(result, PublicKey{Kid: key.kid, Key: key.priv.Public().(ed25519.PublicKey)})
	}
	return result
}
