package sfunode

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/newtspeak/newt-server/backend/internal/model"
)

// DefaultEnrollmentTokenTTL enrollment token 默认有效期（docs 03 §4.2：15–60 分钟）。
const DefaultEnrollmentTokenTTL = 30 * time.Minute

// enrollment 校验失败的具体原因（对外统一 ENROLL_REJECTED，避免泄露内部状态）。
var (
	ErrEnrollTokenMismatch = errors.New("enrollment token 无效")
	ErrEnrollTokenExpired  = errors.New("enrollment token 已过期")
	ErrEnrollTokenUsed     = errors.New("enrollment token 已被使用或未签发")
	ErrEnrollBadStatus     = errors.New("节点状态不允许 enrollment")
)

// NewEnrollmentToken 生成一次性 enrollment token（256bit 随机，仅返回明文一次）与其 SHA-256 哈希。
func NewEnrollmentToken() (token, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("生成 enrollment token 失败: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashEnrollmentToken(token), nil
}

// HashEnrollmentToken 计算 token 的 SHA-256 哈希（库中只存哈希，docs 03 §4.2）。
func HashEnrollmentToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ValidateEnrollment 纯逻辑校验：token 绑定该节点、一次性、未过期、状态为 PENDING_ENROLLMENT。
func ValidateEnrollment(node model.SfuNode, token string, now time.Time) error {
	if node.Status != model.SfuNodePendingEnrollment {
		return ErrEnrollBadStatus
	}
	if node.EnrollmentTokenHash == "" {
		return ErrEnrollTokenUsed
	}
	if node.EnrollmentTokenExpiresAt == nil || now.After(*node.EnrollmentTokenExpiresAt) {
		return ErrEnrollTokenExpired
	}
	if subtle.ConstantTimeCompare([]byte(node.EnrollmentTokenHash), []byte(HashEnrollmentToken(token))) != 1 {
		return ErrEnrollTokenMismatch
	}
	return nil
}
