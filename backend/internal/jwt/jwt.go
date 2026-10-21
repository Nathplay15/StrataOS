// Package jwt signs and verifies HS256 JSON Web Tokens (the only algorithm
// StrataOS needs: OnlyOffice and its own short-lived signed URLs).
package jwt

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrMalformed = errors.New("malformed token")
	ErrAlg       = errors.New("unsupported token algorithm")
	ErrSignature = errors.New("invalid token signature")
	ErrExpired   = errors.New("token expired")
)

var b64 = base64.RawURLEncoding

const header = `{"alg":"HS256","typ":"JWT"}`

func mac(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// Sign encodes claims (any JSON object) as an HS256 token.
func Sign(key []byte, claims any) (string, error) {
	if len(key) == 0 {
		return "", errors.New("empty signing key")
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	data := b64.EncodeToString([]byte(header)) + "." + b64.EncodeToString(body)
	return data + "." + b64.EncodeToString(mac(key, data)), nil
}

// Verify checks the signature and the "exp" claim (when present), then
// decodes the claims into out. Only HS256 is accepted: "none" or asymmetric
// algorithms are refused whatever the header says.
func Verify(key []byte, token string, out any) error {
	return verifyAt(key, token, out, time.Now())
}

func verifyAt(key []byte, token string, out any, now time.Time) error {
	if len(key) == 0 {
		return ErrSignature
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ErrMalformed
	}
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return ErrMalformed
	}
	var h struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(hb, &h) != nil {
		return ErrMalformed
	}
	if h.Alg != "HS256" {
		return ErrAlg
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, mac(key, parts[0]+"."+parts[1])) {
		return ErrSignature
	}
	body, err := b64.DecodeString(parts[1])
	if err != nil {
		return ErrMalformed
	}
	var std struct {
		Exp *float64 `json:"exp"`
	}
	if json.Unmarshal(body, &std) != nil {
		return ErrMalformed
	}
	if std.Exp != nil && now.Unix() >= int64(*std.Exp) {
		return ErrExpired
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return ErrMalformed
		}
	}
	return nil
}
