package oidc

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// JWK is one public key of a provider.
type JWK struct {
	Kid string
	Kty string // RSA, EC, OKP
	Alg string // optional restriction
	Key crypto.PublicKey
}

// KeySet holds the signing keys of a provider.
type KeySet struct{ Keys []JWK }

type rawJWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// ParseJWKS reads a JSON Web Key Set, keeping the signing keys it can use
// (RSA of 2048 bits or more, EC P-256/384/521, Ed25519).
func ParseJWKS(data []byte) (KeySet, error) {
	var set struct {
		Keys []rawJWK `json:"keys"`
	}
	if err := json.Unmarshal(data, &set); err != nil {
		return KeySet{}, fmt.Errorf("invalid key set: %w", err)
	}
	var ks KeySet
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		var pub crypto.PublicKey
		switch k.Kty {
		case "RSA":
			n, err1 := b64.DecodeString(k.N)
			e, err2 := b64.DecodeString(k.E)
			if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
				continue
			}
			key := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
			if key.N.BitLen() < 2048 || key.E < 3 {
				continue
			}
			pub = key
		case "EC":
			var curve elliptic.Curve
			switch k.Crv {
			case "P-256":
				curve = elliptic.P256()
			case "P-384":
				curve = elliptic.P384()
			case "P-521":
				curve = elliptic.P521()
			default:
				continue
			}
			x, err1 := b64.DecodeString(k.X)
			y, err2 := b64.DecodeString(k.Y)
			size := (curve.Params().BitSize + 7) / 8
			if err1 != nil || err2 != nil || len(x) != size || len(y) != size {
				continue
			}
			// Uncompressed point, parsed (and checked on the curve) by crypto/ecdh rules.
			pt := append(append([]byte{4}, x...), y...)
			key, err := ecdsa.ParseUncompressedPublicKey(curve, pt)
			if err != nil {
				continue
			}
			pub = key
		case "OKP":
			x, err := b64.DecodeString(k.X)
			if k.Crv != "Ed25519" || err != nil || len(x) != ed25519.PublicKeySize {
				continue
			}
			pub = ed25519.PublicKey(x)
		default:
			continue
		}
		ks.Keys = append(ks.Keys, JWK{Kid: k.Kid, Kty: k.Kty, Alg: k.Alg, Key: pub})
	}
	if len(ks.Keys) == 0 {
		return KeySet{}, errors.New("the provider publishes no usable signing key")
	}
	return ks, nil
}

var (
	ErrMalformed = errors.New("malformed ID token")
	ErrAlg       = errors.New("ID token algorithm not accepted")
	// ErrNoKey: no key of the set has the token's key ID (rotation: fetch
	// the key set again).
	ErrNoKey     = errors.New("ID token signed with an unknown key")
	ErrSignature = errors.New("invalid ID token signature")
)

// algs maps the accepted algorithms to their key type and hash. "none" and
// the HMAC ones are never accepted.
var algs = map[string]struct {
	kty  string
	hash crypto.Hash
	pss  bool
}{
	"RS256": {"RSA", crypto.SHA256, false}, "RS384": {"RSA", crypto.SHA384, false}, "RS512": {"RSA", crypto.SHA512, false},
	"PS256": {"RSA", crypto.SHA256, true}, "PS384": {"RSA", crypto.SHA384, true}, "PS512": {"RSA", crypto.SHA512, true},
	"ES256": {"EC", crypto.SHA256, false}, "ES384": {"EC", crypto.SHA384, false}, "ES512": {"EC", crypto.SHA512, false},
	"EdDSA": {"OKP", 0, false},
}

func verifySig(alg string, key crypto.PublicKey, signed, sig []byte) bool {
	a := algs[alg]
	var digest []byte
	if a.hash != 0 {
		h := a.hash.New()
		h.Write(signed)
		digest = h.Sum(nil)
	}
	switch k := key.(type) {
	case *rsa.PublicKey:
		if a.kty != "RSA" {
			return false
		}
		if a.pss {
			return rsa.VerifyPSS(k, a.hash, digest, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) == nil
		}
		return rsa.VerifyPKCS1v15(k, a.hash, digest, sig) == nil
	case *ecdsa.PublicKey:
		if a.kty != "EC" {
			return false
		}
		// JWS: r || s, each the size of the curve.
		size := (k.Curve.Params().BitSize + 7) / 8
		want := map[string]int{"ES256": 32, "ES384": 48, "ES512": 66}[alg]
		if size != want || len(sig) != 2*size {
			return false
		}
		r, s := new(big.Int).SetBytes(sig[:size]), new(big.Int).SetBytes(sig[size:])
		return ecdsa.Verify(k, digest, r, s)
	case ed25519.PublicKey:
		return a.kty == "OKP" && ed25519.Verify(k, signed, sig)
	}
	return false
}

// Expect is what an ID token must say.
type Expect struct {
	Issuer   string
	ClientID string
	Nonce    string
	Now      time.Time
	Leeway   time.Duration // clock skew allowed (default 2 minutes)
}

// Claims are the claims of a token or of the userinfo answer.
type Claims map[string]any

// VerifyIDToken checks the signature and the claims of an ID token (OpenID
// Connect Core 3.1.3.7) and returns its claims.
func VerifyIDToken(raw string, ks KeySet, e Expect) (Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || len(raw) > 64<<10 {
		return nil, ErrMalformed
	}
	hb, err1 := b64.DecodeString(parts[0])
	pb, err2 := b64.DecodeString(parts[1])
	sig, err3 := b64.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, ErrMalformed
	}
	var h struct {
		Alg  string          `json:"alg"`
		Kid  string          `json:"kid"`
		Crit json.RawMessage `json:"crit"`
	}
	if err := json.Unmarshal(hb, &h); err != nil {
		return nil, ErrMalformed
	}
	if _, ok := algs[h.Alg]; !ok || len(h.Crit) > 0 {
		return nil, ErrAlg
	}
	signed := []byte(parts[0] + "." + parts[1])
	tried, ok := 0, false
	for _, k := range ks.Keys {
		if (h.Kid != "" && k.Kid != h.Kid) || k.Kty != algs[h.Alg].kty || (k.Alg != "" && k.Alg != h.Alg) {
			continue
		}
		tried++
		if verifySig(h.Alg, k.Key, signed, sig) {
			ok = true
			break
		}
	}
	if tried == 0 {
		return nil, ErrNoKey
	}
	if !ok {
		return nil, ErrSignature
	}
	var c Claims
	dec := json.NewDecoder(bytes.NewReader(pb))
	dec.UseNumber()
	if err := dec.Decode(&c); err != nil {
		return nil, ErrMalformed
	}
	if err := checkClaims(c, e); err != nil {
		return nil, err
	}
	return c, nil
}

func checkClaims(c Claims, e Expect) error {
	leeway := e.Leeway
	if leeway == 0 {
		leeway = 2 * time.Minute
	}
	now := e.Now
	if now.IsZero() {
		now = time.Now()
	}
	if NormalizeIssuer(c.String("iss")) != NormalizeIssuer(e.Issuer) || e.Issuer == "" {
		return errors.New("ID token of another issuer")
	}
	if c.String("sub") == "" {
		return errors.New("ID token without subject")
	}
	aud := c.Strings("aud")
	found := false
	for _, a := range aud {
		if a == e.ClientID {
			found = true
		}
	}
	if !found || e.ClientID == "" {
		return errors.New("ID token for another client")
	}
	if azp := c.String("azp"); (len(aud) > 1 || azp != "") && azp != e.ClientID {
		return errors.New("ID token for another client (azp)")
	}
	exp, ok := c.Time("exp")
	if !ok || !now.Before(exp.Add(leeway)) {
		return errors.New("ID token expired")
	}
	if iat, ok := c.Time("iat"); !ok || iat.After(now.Add(leeway)) {
		return errors.New("ID token issued in the future")
	}
	if nbf, ok := c.Time("nbf"); ok && nbf.After(now.Add(leeway)) {
		return errors.New("ID token not valid yet")
	}
	if e.Nonce == "" || subtle.ConstantTimeCompare([]byte(c.String("nonce")), []byte(e.Nonce)) != 1 {
		return errors.New("ID token nonce mismatch")
	}
	return nil
}

// lookup finds a claim, following dots into objects when there is no claim
// of that exact name (Keycloak: realm_access.roles).
func (c Claims) lookup(name string) (any, bool) {
	if v, ok := c[name]; ok {
		return v, true
	}
	var cur any = map[string]any(c)
	for _, p := range strings.Split(name, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// String returns a string claim ("" when absent or not a string).
func (c Claims) String(name string) string {
	v, _ := c.lookup(name)
	s, _ := v.(string)
	return s
}

// Strings returns a claim holding a string or an array of strings.
func (c Claims) Strings(name string) []string {
	v, _ := c.lookup(name)
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []any:
		var out []string
		for _, i := range x {
			if s, ok := i.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Bool returns a boolean claim (some providers send "true").
func (c Claims) Bool(name string) bool {
	v, _ := c.lookup(name)
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true"
	}
	return false
}

// Time returns a NumericDate claim.
func (c Claims) Time(name string) (time.Time, bool) {
	v, _ := c.lookup(name)
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(int64(f), 0), true
	case float64:
		return time.Unix(int64(x), 0), true
	}
	return time.Time{}, false
}
