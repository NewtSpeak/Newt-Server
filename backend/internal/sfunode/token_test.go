package sfunode

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

func pendingNode(hash string, expires time.Time) model.SfuNode {
	return model.SfuNode{
		ID:                       uuid.New(),
		Status:                   model.SfuNodePendingEnrollment,
		EnrollmentTokenHash:      hash,
		EnrollmentTokenExpiresAt: &expires,
	}
}

func TestEnrollmentTokenEntropyAndHash(t *testing.T) {
	token, hash, err := NewEnrollmentToken()
	if err != nil {
		t.Fatal(err)
	}
	// 256bit 随机 → base64url 43 字符，满足 ≥128bit 要求。
	if len(token) < 22 {
		t.Fatalf("token 熵不足: %q", token)
	}
	if hash != HashEnrollmentToken(token) {
		t.Fatal("哈希不一致")
	}
	if hash == token {
		t.Fatal("库中不允许存 token 明文")
	}
	token2, _, _ := NewEnrollmentToken()
	if token == token2 {
		t.Fatal("两次生成的 token 不应相同")
	}
}

func TestValidateEnrollment(t *testing.T) {
	token, hash, _ := NewEnrollmentToken()
	now := time.Now().UTC()

	t.Run("正常通过", func(t *testing.T) {
		node := pendingNode(hash, now.Add(10*time.Minute))
		if err := ValidateEnrollment(node, token, now); err != nil {
			t.Fatalf("期望通过: %v", err)
		}
	})

	t.Run("token 错误", func(t *testing.T) {
		node := pendingNode(hash, now.Add(10*time.Minute))
		if err := ValidateEnrollment(node, "wrong-token", now); !errors.Is(err, ErrEnrollTokenMismatch) {
			t.Fatalf("期望 ErrEnrollTokenMismatch，得到 %v", err)
		}
	})

	t.Run("过期", func(t *testing.T) {
		node := pendingNode(hash, now.Add(-time.Second))
		if err := ValidateEnrollment(node, token, now); !errors.Is(err, ErrEnrollTokenExpired) {
			t.Fatalf("期望 ErrEnrollTokenExpired，得到 %v", err)
		}
	})

	t.Run("一次性：使用后哈希清空则再次校验失败", func(t *testing.T) {
		node := pendingNode(hash, now.Add(10*time.Minute))
		if err := ValidateEnrollment(node, token, now); err != nil {
			t.Fatalf("首次应通过: %v", err)
		}
		// 模拟 Enroll 成功后的落库状态：哈希清空、状态转 ENROLLED。
		node.EnrollmentTokenHash = ""
		node.Status = model.SfuNodeEnrolled
		if err := ValidateEnrollment(node, token, now); !errors.Is(err, ErrEnrollBadStatus) {
			t.Fatalf("期望 ErrEnrollBadStatus，得到 %v", err)
		}
		// 即使状态被改回 PENDING，哈希已清空仍拒绝。
		node.Status = model.SfuNodePendingEnrollment
		if err := ValidateEnrollment(node, token, now); !errors.Is(err, ErrEnrollTokenUsed) {
			t.Fatalf("期望 ErrEnrollTokenUsed，得到 %v", err)
		}
	})

	t.Run("非 PENDING 状态拒绝", func(t *testing.T) {
		node := pendingNode(hash, now.Add(10*time.Minute))
		node.Status = model.SfuNodeOnline
		if err := ValidateEnrollment(node, token, now); !errors.Is(err, ErrEnrollBadStatus) {
			t.Fatalf("期望 ErrEnrollBadStatus，得到 %v", err)
		}
	})
}
