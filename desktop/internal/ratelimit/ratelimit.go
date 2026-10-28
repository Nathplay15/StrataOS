// Package ratelimit caps the bandwidth of transfers (a token bucket shared
// by every transfer of one direction).
package ratelimit

import (
	"context"
	"io"
	"sync"
	"time"
)

// Limiter allows Rate bytes per second (0 = unlimited), with bursts of one second.
type Limiter struct {
	mu     sync.Mutex
	rate   int64
	tokens float64
	last   time.Time
}

// SetRate changes the limit (bytes per second, 0 = unlimited).
func (l *Limiter) SetRate(bps int64) {
	l.mu.Lock()
	l.rate = bps
	l.mu.Unlock()
}

// Rate returns the limit.
func (l *Limiter) Rate() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rate
}

// Wait blocks until n bytes may pass.
func (l *Limiter) Wait(ctx context.Context, n int) error {
	if l == nil {
		return nil
	}
	for {
		l.mu.Lock()
		if l.rate <= 0 {
			l.mu.Unlock()
			return nil
		}
		now := time.Now()
		if l.last.IsZero() {
			l.last, l.tokens = now, float64(l.rate)
		}
		l.tokens += now.Sub(l.last).Seconds() * float64(l.rate)
		if l.tokens > float64(l.rate) {
			l.tokens = float64(l.rate)
		}
		l.last = now
		need := float64(n)
		if need > float64(l.rate) {
			need = float64(l.rate) // larger than a burst: let it through once full
		}
		if l.tokens >= need {
			l.tokens -= float64(n)
			l.mu.Unlock()
			return nil
		}
		wait := time.Duration((need - l.tokens) / float64(l.rate) * float64(time.Second))
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// Reader limits reads from r.
func Reader(ctx context.Context, r io.Reader, l *Limiter) io.Reader {
	return &reader{ctx, r, l}
}

type reader struct {
	ctx context.Context
	r   io.Reader
	l   *Limiter
}

func (r *reader) Read(p []byte) (int, error) {
	if len(p) > 32<<10 {
		p = p[:32<<10]
	}
	n, err := r.r.Read(p)
	if n > 0 {
		if werr := r.l.Wait(r.ctx, n); werr != nil {
			return n, werr
		}
	}
	return n, err
}
