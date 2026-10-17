// Package totp implements time-based one-time passwords (RFC 6238, RFC 4226).
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	period = 30
	digits = 6
	skew   = 1 // accept one period before/after for clock drift
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random 160-bit secret, base32 encoded.
func NewSecret() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return b32.EncodeToString(b)
}

// URI is the otpauth:// link shown as a QR code to authenticator apps.
func URI(secret, account, issuer string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {fmt.Sprint(digits)}, "period": {fmt.Sprint(period)}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func code(key []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", digits, v%1_000_000)
}

// Code returns the current code for a secret (used by tests).
func Code(secret string, t time.Time) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	return code(key, uint64(t.Unix()/period)), nil
}

// Verify checks a code. last is the counter of the previously accepted code, so a
// code cannot be replayed; the returned counter must be stored on success.
func Verify(secret, input string, t time.Time, last int64) (bool, int64) {
	input = strings.ReplaceAll(strings.TrimSpace(input), " ", "")
	if len(input) != digits {
		return false, last
	}
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return false, last
	}
	now := t.Unix() / period
	for d := -skew; d <= skew; d++ {
		c := now + int64(d)
		if c <= last {
			continue
		}
		if hmac.Equal([]byte(code(key, uint64(c))), []byte(input)) {
			return true, c
		}
	}
	return false, last
}
