package activity

// 按用户令牌桶（复制自 message/ratelimit.go 并补充闲置淘汰，避免 map 只增不减）。

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

type tokenBucket struct {
	tokens   float64
	lastFill time.Time
}

type userLimiter struct {
	mu      sync.Mutex
	buckets map[uuid.UUID]*tokenBucket
	rate    float64 // 每秒补充令牌数
	burst   float64 // 桶容量（突发上限）
	now     func() time.Time
}

func newUserLimiter(rate, burst float64) *userLimiter {
	return &userLimiter{
		buckets: make(map[uuid.UUID]*tokenBucket),
		rate:    rate,
		burst:   burst,
		now:     time.Now,
	}
}

// Allow 尝试为该用户消耗一个令牌；桶空则拒绝。
func (l *userLimiter) Allow(userID uuid.UUID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	bucket, ok := l.buckets[userID]
	if !ok {
		bucket = &tokenBucket{tokens: l.burst, lastFill: now}
		l.buckets[userID] = bucket
	}
	elapsed := now.Sub(bucket.lastFill).Seconds()
	if elapsed > 0 {
		bucket.tokens += elapsed * l.rate
		if bucket.tokens > l.burst {
			bucket.tokens = l.burst
		}
		bucket.lastFill = now
	}
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

// prune 淘汰闲置超过 maxIdle 的桶（flush 周期顺带调用）。
func (l *userLimiter) prune(maxIdle time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-maxIdle)
	for id, bucket := range l.buckets {
		if bucket.lastFill.Before(cutoff) {
			delete(l.buckets, id)
		}
	}
}
