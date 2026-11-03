package gate

import (
	"sync"
	"time"
)

// RateLimiter is a token bucket per key (a client address, a link): Burst
// requests at once, then one every Every. Idle keys are forgotten.
type RateLimiter struct {
	Burst int
	Every time.Duration
	Now   func() time.Time // nil: time.Now

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

// maxKeys bounds the memory a flood of addresses can take.
const maxKeys = 20000

func (l *RateLimiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Allow takes n tokens for key and reports whether there were enough.
func (l *RateLimiter) Allow(key string, n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.buckets == nil {
		l.buckets = map[string]*bucket{}
	}
	full := float64(l.Burst)
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= maxKeys {
			l.pruneLocked(now)
		}
		b = &bucket{tokens: full, at: now}
		l.buckets[key] = b
	}
	if el := now.Sub(b.at); el > 0 && l.Every > 0 {
		b.tokens = min(full, b.tokens+float64(el)/float64(l.Every))
	}
	b.at = now
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}

// pruneLocked drops the buckets that are full again; if a flood leaves none
// to drop, it starts over rather than grow without bound.
func (l *RateLimiter) pruneLocked(now time.Time) {
	refill := time.Duration(l.Burst) * l.Every
	for k, b := range l.buckets {
		if now.Sub(b.at) >= refill {
			delete(l.buckets, k)
		}
	}
	if len(l.buckets) >= maxKeys {
		l.buckets = map[string]*bucket{}
	}
}
