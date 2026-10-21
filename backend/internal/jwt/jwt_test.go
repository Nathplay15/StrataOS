package jwt

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	key := []byte("secret-key")
	tok, err := Sign(key, map[string]any{"path": "/srv/strata/a.docx", "exp": time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := Verify(key, tok, &out); err != nil || out.Path != "/srv/strata/a.docx" {
		t.Fatalf("verify: %v %q", err, out.Path)
	}
	// Known vector (jwt.io): header and payload are re-signed with our key.
	h := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"
	p := "eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ"
	sig := "SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	if err := Verify([]byte("your-256-bit-secret"), h+"."+p+"."+sig, nil); err != nil {
		t.Fatalf("jwt.io vector: %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	key := []byte("k")
	now := time.Now()
	valid, _ := Sign(key, map[string]any{"a": 1})
	expired, _ := Sign(key, map[string]any{"exp": now.Add(-time.Second).Unix()})
	parts := strings.Split(valid, ".")
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + parts[1] + "."
	// alg confusion: HS512 header signed as if HS256.
	h512 := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS512"}`))
	m := hmac.New(sha256.New, key)
	m.Write([]byte(h512 + "." + parts[1]))
	hs512 := h512 + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"a":2}`)) + "." + parts[2]
	cases := []struct {
		name, tok string
		key       []byte
		want      error
	}{
		{"wrong key", valid, []byte("other"), ErrSignature},
		{"empty key", valid, nil, ErrSignature},
		{"expired", expired, key, ErrExpired},
		{"alg none", none, key, ErrAlg},
		{"other alg", hs512, key, ErrAlg},
		{"tampered payload", tampered, key, ErrSignature},
		{"two parts", parts[0] + "." + parts[1], key, ErrMalformed},
		{"garbage", "a.b.c", key, ErrMalformed},
	}
	for _, c := range cases {
		if err := Verify(c.key, c.tok, nil); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
	if err := verifyAt(key, expired, nil, now.Add(-time.Hour)); err != nil {
		t.Errorf("not yet expired: %v", err)
	}
	if _, err := Sign(nil, map[string]any{}); err == nil {
		t.Error("empty key accepted for signing")
	}
}
