// Package webpush sends Web Push messages (RFC 8030): payload encryption
// (RFC 8291, aes128gcm of RFC 8188) and VAPID authentication (RFC 8292).
// UnifiedPush distributors (ntfy, NextPush…) take the same requests.
package webpush

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// recordSize is the rs field of the aes128gcm header: one record holds the
// whole message (push services accept up to 4096 bytes of payload).
const recordSize = 4096

// MaxPlaintext leaves room for the header, the delimiter and the GCM tag.
const MaxPlaintext = 3800

var b64 = base64.RawURLEncoding

// decodeB64 accepts base64url with or without padding (apps differ).
func decodeB64(s string) ([]byte, error) {
	for len(s)%4 != 0 && len(s) > 0 && s[len(s)-1] == '=' {
		s = s[:len(s)-1]
	}
	if b, err := b64.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// Encrypt encrypts plaintext for a subscription (p256dh: the client's
// uncompressed P-256 public key, auth: its 16-byte secret, both base64url).
func Encrypt(plaintext []byte, p256dh, auth string) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return encrypt(plaintext, p256dh, auth, salt, as)
}

func encrypt(plaintext []byte, p256dh, auth string, salt []byte, as *ecdh.PrivateKey) ([]byte, error) {
	if len(plaintext) > MaxPlaintext {
		return nil, errors.New("message too long")
	}
	uaBytes, err := decodeB64(p256dh)
	if err != nil {
		return nil, errors.New("invalid p256dh key")
	}
	ua, err := ecdh.P256().NewPublicKey(uaBytes)
	if err != nil {
		return nil, errors.New("invalid p256dh key")
	}
	secret, err := decodeB64(auth)
	if err != nil || len(secret) != 16 {
		return nil, errors.New("invalid auth secret")
	}
	shared, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()
	// RFC 8291 §3.4: IKM from the ECDH secret and the auth secret.
	info := append(append([]byte("WebPush: info\x00"), uaBytes...), asPub...)
	ikm, err := hkdf.Key(sha256.New, shared, secret, string(info), 32)
	if err != nil {
		return nil, err
	}
	// RFC 8188 §2.2: content encryption key and nonce.
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(salt)
	_ = binary.Write(&out, binary.BigEndian, uint32(recordSize))
	out.WriteByte(byte(len(asPub)))
	out.Write(asPub)
	// A single, last record: the plaintext followed by the 0x02 delimiter.
	out.Write(gcm.Seal(nil, nonce, append(append([]byte{}, plaintext...), 2), nil))
	return out.Bytes(), nil
}

// VAPID is the key identifying the application server to push services.
type VAPID struct {
	key *ecdsa.PrivateKey
}

// NewVAPIDKey returns a new private key, PKCS #8 in base64.
func NewVAPIDKey() (string, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// ParseVAPID loads a key made by NewVAPIDKey.
func ParseVAPID(s string) (*VAPID, error) {
	der, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok || ek.Curve != elliptic.P256() {
		return nil, errors.New("not a P-256 key")
	}
	return &VAPID{key: ek}, nil
}

// PublicKey is the uncompressed public key, base64url (what apps pass to
// their distributor and the k= parameter of the Authorization header).
func (v *VAPID) PublicKey() string {
	pk, err := v.key.PublicKey.ECDH()
	if err != nil {
		return ""
	}
	return b64.EncodeToString(pk.Bytes())
}

// header returns the Authorization header for an endpoint (RFC 8292 §3).
func (v *VAPID) header(endpoint, subject string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	hdr := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": now.Add(12 * time.Hour).Unix(), "sub": subject})
	unsigned := hdr + "." + b64.EncodeToString(claims)
	sum := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, v.key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + unsigned + "." + b64.EncodeToString(sig) + ", k=" + v.PublicKey(), nil
}

// verify checks a JWT signed by header (tests).
func (v *VAPID) verify(jwt string) bool {
	i := bytes.LastIndexByte([]byte(jwt), '.')
	if i < 0 {
		return false
	}
	sig, err := b64.DecodeString(jwt[i+1:])
	if err != nil || len(sig) != 64 {
		return false
	}
	sum := sha256.Sum256([]byte(jwt[:i]))
	return ecdsa.Verify(&v.key.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

// Message is one push to one endpoint.
type Message struct {
	Endpoint string
	// P256DH and Auth: the subscription keys; empty sends the payload as is
	// (distributors that do not encrypt, such as a bare ntfy topic).
	P256DH, Auth string
	Payload      []byte
	TTL          time.Duration
	Urgency      string // very-low | low | normal | high
}

// ErrGone: the endpoint no longer exists (the app was uninstalled or
// unregistered); the subscription should be dropped.
var ErrGone = errors.New("push endpoint gone")

// Send delivers m. vapid may be nil (no Authorization header).
func Send(cl *http.Client, m Message, vapid *VAPID, subject string) error {
	u, err := url.Parse(m.Endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("invalid push endpoint")
	}
	body := m.Payload
	encrypted := m.P256DH != "" && m.Auth != ""
	if encrypted {
		if body, err = Encrypt(m.Payload, m.P256DH, m.Auth); err != nil {
			return err
		}
	}
	req, err := http.NewRequest("POST", m.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	ttl := m.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	req.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	if m.Urgency != "" {
		req.Header.Set("Urgency", m.Urgency)
	}
	if encrypted {
		req.Header.Set("Content-Encoding", "aes128gcm")
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	if vapid != nil {
		h, err := vapid.header(m.Endpoint, subject, time.Now())
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", h)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	case resp.StatusCode >= 300:
		return fmt.Errorf("push service answered HTTP %d", resp.StatusCode)
	}
	return nil
}
