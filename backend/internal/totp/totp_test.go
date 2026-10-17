package totp

import (
	"encoding/base32"
	"testing"
	"time"
)

// RFC 6238 appendix B test vector (SHA1, secret "12345678901234567890").
func TestRFCVector(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		got, err := Code(secret, time.Unix(ts, 0))
		if err != nil || got != want {
			t.Errorf("t=%d: got %s, want %s", ts, got, want)
		}
	}
}

func TestVerify(t *testing.T) {
	s := NewSecret()
	now := time.Unix(1_800_000_000, 0)
	c, _ := Code(s, now)
	ok, counter := Verify(s, c, now, 0)
	if !ok {
		t.Fatal("valid code rejected")
	}
	if ok, _ := Verify(s, c, now, counter); ok {
		t.Error("replayed code accepted")
	}
	prev, _ := Code(s, now.Add(-30*time.Second))
	if ok, _ := Verify(s, prev, now, 0); !ok {
		t.Error("code from previous period (clock drift) rejected")
	}
	old, _ := Code(s, now.Add(-5*time.Minute))
	if ok, _ := Verify(s, old, now, 0); ok {
		t.Error("old code accepted")
	}
	if ok, _ := Verify(s, "12345", now, 0); ok {
		t.Error("short code accepted")
	}
}
