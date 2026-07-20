package message

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// 搜索按用户限流（docs 13 AU.8）：简单令牌桶，每用户默认 1 QPS、突发 5。

type tokenBucket struct {
	tokens   float64
	lastFill time.Time
}

// userLimiter 按用户维护令牌桶的限流器。
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
