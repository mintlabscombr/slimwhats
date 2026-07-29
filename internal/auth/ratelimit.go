package auth

import (
	"sync"
	"time"
)

// LoginRateLimiter is a simple in-memory sliding-window rate limiter
// keyed by source IP. Defaults: 5 failed attempts per 10 minutes per IP.
// On lockout the IP is blocked for 15 minutes (the request returns 429
// with Retry-After = remaining lockout seconds).
type LoginRateLimiter struct {
	mu          sync.Mutex
	failures    map[string][]time.Time
	lockedUntil map[string]time.Time
	maxFailures int
	window      time.Duration
	lockout     time.Duration
}

// NewLoginRateLimiter returns a limiter with the PRD defaults.
func NewLoginRateLimiter() *LoginRateLimiter {
	return &LoginRateLimiter{
		failures:    make(map[string][]time.Time),
		lockedUntil: make(map[string]time.Time),
		maxFailures: 5,
		window:      10 * time.Minute,
		lockout:     15 * time.Minute,
	}
}

// Check returns the lockout-remaining time for ip. If zero, the caller
// may proceed with login.
func (l *LoginRateLimiter) Check(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until, ok := l.lockedUntil[ip]; ok {
		if remaining := time.Until(until); remaining > 0 {
			return remaining
		}
		delete(l.lockedUntil, ip)
	}
	return 0
}

// RecordFailure logs a failed login attempt for ip. If the count in the
// rolling window reaches maxFailures, the IP is locked for the lockout
// duration. Returns the new lockout-remaining (0 if not locked).
func (l *LoginRateLimiter) RecordFailure(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)
	// prune old entries
	failures := l.failures[ip]
	keep := failures[:0]
	for _, t := range failures {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	keep = append(keep, now)
	l.failures[ip] = keep
	if len(keep) >= l.maxFailures {
		l.lockedUntil[ip] = now.Add(l.lockout)
		return l.lockout
	}
	return 0
}

// RecordSuccess clears any failure history for ip (e.g. on successful login).
func (l *LoginRateLimiter) RecordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
	delete(l.lockedUntil, ip)
}
