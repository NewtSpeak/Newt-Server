package security

import (
	"sync"
	"time"
)

type attemptWindow struct {
	count     int
	expiresAt time.Time
}

type LoginLimiter struct {
	mu              sync.Mutex
	byIdentifier    map[string]attemptWindow
	byIP            map[string]attemptWindow
	identifierLimit int
	ipLimit         int
	window          time.Duration
	now             func() time.Time
}

func NewLoginLimiter(identifierLimit, ipLimit int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{
		byIdentifier:    make(map[string]attemptWindow),
		byIP:            make(map[string]attemptWindow),
		identifierLimit: identifierLimit,
		ipLimit:         ipLimit,
		window:          window,
		now:             time.Now,
	}
}

func (l *LoginLimiter) Allow(ip, identifier string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	identifierAttempt := l.active(l.byIdentifier, identifier, now)
	ipAttempt := l.active(l.byIP, ip, now)
	if identifierAttempt.count >= l.identifierLimit {
		return false, identifierAttempt.expiresAt.Sub(now).Truncate(time.Second)
	}
	if ipAttempt.count >= l.ipLimit {
		return false, ipAttempt.expiresAt.Sub(now).Truncate(time.Second)
	}
	return true, 0
}

func (l *LoginLimiter) Failure(ip, identifier string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.increment(l.byIdentifier, identifier, now)
	l.increment(l.byIP, ip, now)
}

func (l *LoginLimiter) Success(identifier string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIdentifier, identifier)
}

func (l *LoginLimiter) active(store map[string]attemptWindow, key string, now time.Time) attemptWindow {
	attempt := store[key]
	if !attempt.expiresAt.After(now) {
		delete(store, key)
		return attemptWindow{}
	}
	return attempt
}

func (l *LoginLimiter) increment(store map[string]attemptWindow, key string, now time.Time) {
	attempt := l.active(store, key, now)
	if attempt.count == 0 {
		attempt.expiresAt = now.Add(l.window)
	}
	attempt.count++
	store[key] = attempt
}
