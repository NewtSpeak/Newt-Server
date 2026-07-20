package sfucontrol

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"time"

	"github.com/owlspeak/owl-server/backend/internal/model"
)

var (
	errEnrollWrongStatus  = errors.New("节点不处于待接入状态")
	errEnrollTokenUsed    = errors.New("enrollment token 已使用或未签发")
	errEnrollTokenBad     = errors.New("enrollment token 不匹配")
	errEnrollTokenExpired = errors.New("enrollment token 已过期")
)

// HashEnrollmentToken enrollment token 的存储哈希（sha256 hex，docs 03 §4.2）。
func HashEnrollmentToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// validateEnrollment 校验一次性 enrollment token：状态、哈希匹配、未过期。
// 一次性语义由调用方在成功后清空 EnrollmentTokenHash 保证。
func validateEnrollment(node *model.SfuNode, token string, now time.Time) error {
	if node.Status != model.SfuNodePendingEnrollment {
		return errEnrollWrongStatus
	}
	if node.EnrollmentTokenHash == "" {
		return errEnrollTokenUsed
	}
	if node.EnrollmentTokenExpiresAt == nil || now.After(*node.EnrollmentTokenExpiresAt) {
		return errEnrollTokenExpired
	}
	expected := node.EnrollmentTokenHash
	actual := HashEnrollmentToken(token)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) != 1 {
		return errEnrollTokenBad
	}
	return nil
}

// applyEnrollment 成功签发后的节点状态迁移：ENROLLED + 指纹 + token 作废（一次性）。
func applyEnrollment(node *model.SfuNode, fingerprint string, notAfter time.Time) {
	node.Status = model.SfuNodeEnrolled
	node.CertFingerprint = fingerprint
	node.CertNotAfter = &notAfter
	node.EnrollmentTokenHash = ""
	node.EnrollmentTokenExpiresAt = nil
}
