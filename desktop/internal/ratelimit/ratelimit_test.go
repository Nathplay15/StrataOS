package ratelimit

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	var l Limiter
	l.SetRate(100 << 10) // 100 KB/s
	start := time.Now()
	n, _ := io.Copy(io.Discard, Reader(context.Background(), bytes.NewReader(make([]byte, 250<<10)), &l))
	if n != 250<<10 {
		t.Fatal(n)
	}
	// One second of burst, then 150 KB at 100 KB/s.
	if d := time.Since(start); d < 1300*time.Millisecond || d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
	l.SetRate(0)
	start = time.Now()
	io.Copy(io.Discard, Reader(context.Background(), bytes.NewReader(make([]byte, 10<<20)), &l))
	if time.Since(start) > time.Second {
		t.Fatal("unlimited is slow")
	}
}
