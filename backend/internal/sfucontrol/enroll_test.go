package sfucontrol

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

func pendingNode(token string, expiresIn time.Duration) *model.SfuNode {
	expiresAt := time.Now().Add(expiresIn)
	return &model.SfuNode{
		ID:                       uuid.New(),
		Status:                   model.SfuNodePendingEnrollment,
		EnrollmentTokenHash:      HashEnrollmentToken(token),
		EnrollmentTokenExpiresAt: &expiresAt,
	}
}

func TestValidateEnrollmentSuccess(t *testing.T) {
	node := pendingNode("secret-token", 30*time.Minute)
	if err := validateEnrollment(node, "secret-token", time.Now()); err != nil {
		t.Fatalf("合法 token 校验失败: %v", err)
	}
}

func TestValidateEnrollmentWrongToken(t *testing.T) {
	node := pendingNode("secret-token", 30*time.Minute)
	if err := validateEnrollment(node, "wrong-token", time.Now()); !errors.Is(err, errEnrollTokenBad) {
		t.Fatalf("错误 token 应返回 errEnrollTokenBad，实际 %v", err)
	}
}

func TestValidateEnrollmentExpired(t *testing.T) {
	node := pendingNode("secret-token", -1*time.Minute)
	if err := validateEnrollment(node, "secret-token", time.Now()); !errors.Is(err, errEnrollTokenExpired) {
		t.Fatalf("过期 token 应返回 errEnrollTokenExpired，实际 %v", err)
	}
}

func TestValidateEnrollmentWrongStatus(t *testing.T) {
	node := pendingNode("secret-token", 30*time.Minute)
	node.Status = model.SfuNodeEnrolled
	if err := validateEnrollment(node, "secret-token", time.Now()); !errors.Is(err, errEnrollWrongStatus) {
		t.Fatalf("非 PENDING 状态应返回 errEnrollWrongStatus，实际 %v", err)
	}
}

func TestEnrollmentTokenOneTime(t *testing.T) {
	node := pendingNode("secret-token", 30*time.Minute)
	if err := validateEnrollment(node, "secret-token", time.Now()); err != nil {
		t.Fatal(err)
	}
	// 成功签发后 applyEnrollment 清空哈希 → 同一 token 再来必须被拒绝。
	applyEnrollment(node, "abcd", time.Now().Add(90*24*time.Hour))
	if node.EnrollmentTokenHash != "" || node.EnrollmentTokenExpiresAt != nil {
		t.Fatal("applyEnrollment 必须清空 token 哈希与过期时间")
	}
	if node.Status != model.SfuNodeEnrolled {
		t.Fatalf("状态应迁移为 ENROLLED，实际 %s", node.Status)
	}
	if err := validateEnrollment(node, "secret-token", time.Now()); err == nil {
		t.Fatal("token 用完后重放必须被拒绝")
	}
}
