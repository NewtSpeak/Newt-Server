package security

import (
	"testing"
	"time"
)

func TestLoginLimiterBlocksIdentifier(t *testing.T) {
	now := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter(2, 10, 15*time.Minute)
	limiter.now = func() time.Time { return now }
	limiter.Failure("127.0.0.1", "user@example.com")
	limiter.Failure("127.0.0.2", "user@example.com")
	if allowed, retry := limiter.Allow("127.0.0.3", "user@example.com"); allowed || retry <= 0 {
		t.Fatal("达到账号失败阈值后应拒绝并返回重试时间")
	}
	limiter.Success("user@example.com")
	if allowed, _ := limiter.Allow("127.0.0.3", "user@example.com"); !allowed {
		t.Fatal("登录成功后应清除账号失败计数")
	}
}

func TestLoginLimiterBlocksIPAndExpires(t *testing.T) {
	now := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter(10, 2, time.Minute)
	limiter.now = func() time.Time { return now }
	limiter.Failure("127.0.0.1", "first")
	limiter.Failure("127.0.0.1", "second")
	if allowed, _ := limiter.Allow("127.0.0.1", "third"); allowed {
		t.Fatal("达到 IP 失败阈值后应拒绝")
	}
	now = now.Add(time.Minute + time.Second)
	if allowed, _ := limiter.Allow("127.0.0.1", "third"); !allowed {
		t.Fatal("窗口到期后应恢复")
	}
}
