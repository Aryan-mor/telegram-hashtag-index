package service

import (
	"sync"
	"time"
)

// limiter is a token bucket per key. It holds `limit` tokens, refilled evenly over a minute.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter() *limiter { return &limiter{buckets: map[string]*bucket{}} }

// take asks for n tokens and returns how many it granted, between 0 and n. Only whole
// tokens are granted. A limit of zero or less means no limit.
func (l *limiter) take(key string, n, limit int, now time.Time) int {
	if limit <= 0 {
		return n
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: float64(limit), at: now}
		l.buckets[key] = b
	}
	if el := now.Sub(b.at); el > 0 {
		b.tokens += el.Seconds() * float64(limit) / 60
		if b.tokens > float64(limit) {
			b.tokens = float64(limit)
		}
		b.at = now
	}
	grant := int(b.tokens)
	if grant > n {
		grant = n
	}
	b.tokens -= float64(grant)
	return grant
}
