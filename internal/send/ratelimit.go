package send

import (
	"sync"
	"time"
)

// PerJIDRateLimiter is a simple token bucket keyed by recipient JID.
// 20 messages per 60s, configurable. Excess returns a Retry-After hint.
type PerJIDRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	max     int
	window  time.Duration
}

type bucket struct {
	timestamps []time.Time
}

// NewPerJIDRateLimiter returns the PRD-default limiter (20 / 60s).
func NewPerJIDRateLimiter() *PerJIDRateLimiter {
	return &PerJIDRateLimiter{
		buckets: make(map[string]*bucket),
		max:     20,
		window:  60 * time.Second,
	}
}

// Check returns (true, 0) if a send to jid is allowed, or (false,
// retryAfter) if rate-limited. On allow, the call counts as a send.
func (l *PerJIDRateLimiter) Check(jid string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)
	b, ok := l.buckets[jid]
	if !ok {
		b = &bucket{}
		l.buckets[jid] = b
	}
	// Prune old
	keep := b.timestamps[:0]
	for _, t := range b.timestamps {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	b.timestamps = keep
	if len(b.timestamps) >= l.max {
		// Retry-after = time until the oldest timestamp falls out of the window
		oldest := b.timestamps[0]
		retryAfter := oldest.Add(l.window).Sub(now)
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		return false, retryAfter
	}
	b.timestamps = append(b.timestamps, now)
	return true, 0
}
