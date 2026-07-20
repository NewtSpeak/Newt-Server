package message

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestUserLimiterBurst 突发容量内放行，耗尽后拒绝（AU.8：1 QPS 突发 5）。
func TestUserLimiterBurst(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := newUserLimiter(1, 5)
	limiter.now = func() time.Time { return now }
	user := uuid.New()
	for i := 0; i < 5; i++ {
		if !limiter.Allow(user) {
			t.Fatalf("突发容量内第 %d 次请求不应被拒", i+1)
		}
	}
	if limiter.Allow(user) {
		t.Fatal("桶耗尽后应被拒绝")
	}
}

// TestUserLimiterRefill 令牌按速率补充，且不超过桶容量。
func TestUserLimiterRefill(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := newUserLimiter(1, 5)
	limiter.now = func() time.Time { return now }
	user := uuid.New()
	for i := 0; i < 5; i++ {
		limiter.Allow(user)
	}
	if limiter.Allow(user) {
		t.Fatal("桶应已耗尽")
	}
	// 1 秒补 1 个令牌。
	now = now.Add(time.Second)
	if !limiter.Allow(user) {
		t.Fatal("补充 1 个令牌后应放行 1 次")
	}
	if limiter.Allow(user) {
		t.Fatal("仅补充了 1 个令牌，第 2 次应被拒")
	}
	// 长时间空闲后至多回到桶容量 5。
	now = now.Add(time.Hour)
	allowed := 0
	for i := 0; i < 10; i++ {
		if limiter.Allow(user) {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("长时间空闲后应恰好放行 5 次（桶容量），实际 %d", allowed)
	}
}

// TestUserLimiterIsolation 不同用户互不影响。
func TestUserLimiterIsolation(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := newUserLimiter(1, 5)
	limiter.now = func() time.Time { return now }
	userA, userB := uuid.New(), uuid.New()
	for i := 0; i < 5; i++ {
		limiter.Allow(userA)
	}
	if limiter.Allow(userA) {
		t.Fatal("用户 A 桶应已耗尽")
	}
	if !limiter.Allow(userB) {
		t.Fatal("用户 B 不应受用户 A 限流影响")
	}
}
