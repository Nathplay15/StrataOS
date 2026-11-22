// Package sso makes the NAS an OpenID Connect provider for its apps
// (authorization code with PKCE, ID tokens signed with RS256, userinfo,
// refresh tokens, RP-initiated logout) and keeps the sessions of the forward
// authentication of the reverse proxy. It is pure apart from its own files:
// accounts, groups and access rules are given by the caller (api/sso*.go).
package sso

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"slices"
	"strings"
	"time"
)

var b64 = base64.RawURLEncoding

// Key rotation: the next key is published before it signs, so relying
// parties that cache the key set already know it; a retired key stays
// published long enough for every token it signed to expire.
const (
	RotateEvery = 90 * 24 * time.Hour
	KeepRetired = 14 * 24 * time.Hour
	keyBits     = 2048
)

// Key is one RSA signing key. State: "next" (published, not signing yet),
// "active" (signs) or "retired" (published until Retired+KeepRetired).
type Key struct {
	ID      string    `json:"id"`
	State   string    `json:"state"`
	Created time.Time `json:"created"`
	Active  time.Time `json:"active,omitzero"`
	Retired time.Time `json:"retired,omitzero"`
	PEM     string    `json:"pem"` // PKCS#8 private key

	priv *rsa.PrivateKey
}

// KeyRing holds the keys, newest first.
type KeyRing struct {
	Keys []*Key `json:"keys"`
}

func newKey(now time.Time) (*Key, error) {
	priv, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	pub, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	h := sha256.Sum256(pub)
	return &Key{ID: hex.EncodeToString(h[:8]), State: "next", Created: now, priv: priv,
		PEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))}, nil
}

func (k *Key) private() (*rsa.PrivateKey, error) {
	if k.priv != nil {
		return k.priv, nil
	}
	b, _ := pem.Decode([]byte(k.PEM))
	if b == nil {
		return nil, errors.New("invalid signing key")
	}
	p, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := p.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("signing key is not RSA")
	}
	k.priv = rk
	return rk, nil
}

func (r *KeyRing) find(state string) *Key {
	for _, k := range r.Keys {
		if k.State == state {
			return k
		}
	}
	return nil
}

// Maintain makes sure there is an active and a next key, rotates the active
// one after RotateEvery and drops retired keys after KeepRetired. It
// reports whether the ring changed (and must be saved).
func (r *KeyRing) Maintain(now time.Time) (bool, error) {
	changed := false
	if r.find("active") == nil {
		if n := r.find("next"); n != nil {
			n.State, n.Active = "active", now
		} else {
			k, err := newKey(now)
			if err != nil {
				return false, err
			}
			k.State, k.Active = "active", now
			r.Keys = append([]*Key{k}, r.Keys...)
		}
		changed = true
	}
	if a := r.find("active"); now.Sub(a.Active) >= RotateEvery {
		if err := r.Rotate(now); err != nil {
			return changed, err
		}
		changed = true
	}
	if r.find("next") == nil {
		k, err := newKey(now)
		if err != nil {
			return changed, err
		}
		r.Keys = append([]*Key{k}, r.Keys...)
		changed = true
	}
	n := len(r.Keys)
	r.Keys = slices.DeleteFunc(r.Keys, func(k *Key) bool {
		return k.State == "retired" && now.Sub(k.Retired) > KeepRetired
	})
	return changed || n != len(r.Keys), nil
}

// Rotate retires the active key and makes the next one active (it was
// already published); a new next key is made.
func (r *KeyRing) Rotate(now time.Time) error {
	k, err := newKey(now)
	if err != nil {
		return err
	}
	if a := r.find("active"); a != nil {
		a.State, a.Retired = "retired", now
	}
	if n := r.find("next"); n != nil {
		n.State, n.Active = "active", now
	} else {
		k.State, k.Active = "active", now
		r.Keys = append([]*Key{k}, r.Keys...)
		if k, err = newKey(now); err != nil {
			return err
		}
	}
	r.Keys = append([]*Key{k}, r.Keys...)
	return nil
}

type jwk struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKS is the public key set (every published key).
func (r *KeyRing) JWKS() []byte {
	set := struct {
		Keys []jwk `json:"keys"`
	}{Keys: []jwk{}}
	for _, k := range r.Keys {
		p, err := k.private()
		if err != nil {
			continue
		}
		set.Keys = append(set.Keys, jwk{Kty: "RSA", Use: "sig", Alg: "RS256", Kid: k.ID,
			N: b64.EncodeToString(p.N.Bytes()), E: b64.EncodeToString(big.NewInt(int64(p.E)).Bytes())})
	}
	b, _ := json.Marshal(set)
	return b
}

// Sign makes a JWT of claims with the active key.
func (r *KeyRing) Sign(claims any) (string, error) {
	a := r.find("active")
	if a == nil {
		return "", errors.New("no signing key")
	}
	p, err := a.private()
	if err != nil {
		return "", err
	}
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": a.ID, "typ": "JWT"})
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	data := b64.EncodeToString(hdr) + "." + b64.EncodeToString(body)
	h := sha256.Sum256([]byte(data))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p, crypto.SHA256, h[:])
	if err != nil {
		return "", err
	}
	return data + "." + b64.EncodeToString(sig), nil
}

// Errors of Verify.
var (
	ErrMalformed = errors.New("malformed token")
	ErrSignature = errors.New("invalid token signature")
	ErrExpired   = errors.New("token expired")
)

// Verify checks a token signed by one of the published keys and decodes its
// claims; with checkExp, an expired token is refused (an id_token_hint of
// the logout may be expired).
func (r *KeyRing) Verify(token string, out any, now time.Time, checkExp bool) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ErrMalformed
	}
	hb, err1 := b64.DecodeString(parts[0])
	body, err2 := b64.DecodeString(parts[1])
	sig, err3 := b64.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return ErrMalformed
	}
	var h struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(hb, &h) != nil || h.Alg != "RS256" {
		return ErrSignature
	}
	var key *Key
	for _, k := range r.Keys {
		if k.ID == h.Kid {
			key = k
		}
	}
	if key == nil {
		return ErrSignature
	}
	p, err := key.private()
	if err != nil {
		return ErrSignature
	}
	d := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&p.PublicKey, crypto.SHA256, d[:], sig) != nil {
		return ErrSignature
	}
	var std struct {
		Exp *float64 `json:"exp"`
	}
	if json.Unmarshal(body, &std) != nil {
		return ErrMalformed
	}
	if checkExp && (std.Exp == nil || now.Unix() >= int64(*std.Exp)) {
		return ErrExpired
	}
	if out != nil && json.Unmarshal(body, out) != nil {
		return ErrMalformed
	}
	return nil
}

// KeyInfo describes a key for the administration page.
type KeyInfo struct {
	ID      string    `json:"id"`
	State   string    `json:"state"`
	Created time.Time `json:"created"`
	Active  time.Time `json:"active,omitzero"`
	Retired time.Time `json:"retired,omitzero"`
}

// Info lists the keys without their private part.
func (r *KeyRing) Info() []KeyInfo {
	var out []KeyInfo
	for _, k := range r.Keys {
		out = append(out, KeyInfo{ID: k.ID, State: k.State, Created: k.Created, Active: k.Active, Retired: k.Retired})
	}
	return out
}
