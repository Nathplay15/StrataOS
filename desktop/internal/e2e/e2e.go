// Package e2e encrypts the files of end-to-end encrypted sync folders: the
// NAS stores only ciphertext. A folder has a random 256-bit master key,
// sealed in the folder's header (.strataos-sync-e2e.json on the NAS) with a
// key derived from the passphrase by Argon2id. The recovery key is the
// master key itself, written in base32 with a checksum.
//
// From the master key, HKDF-SHA256 derives a content key and two name keys.
// File contents (format v1):
//
//	"SE2E" | 0x01 | prefix (16 random bytes)                     header, 21 bytes
//	XChaCha20-Poly1305(nonce = prefix | 2^64-1, size | sha256)   metadata, 56 bytes
//	XChaCha20-Poly1305(nonce = prefix | i, chunk i, ad = header | last)  chunks of 64 KiB
//
// The header is authenticated with every chunk, the chunk number is in its
// nonce (no reordering) and the last chunk is marked (no truncation). The
// metadata gives the plaintext size and SHA-256 without downloading the
// file. Names are encrypted one path component at a time, deterministically
// (a synthetic IV: nonce = HMAC(name)), and written in unpadded base64url.
package e2e

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// Header is the header of an encrypted folder, as stored on the NAS.
type Header struct {
	V       int    `json:"v"`
	ID      string `json:"id"`
	Cipher  string `json:"cipher"`
	KDF     KDF    `json:"kdf"`
	Wrapped string `json:"wrapped"`
	Check   string `json:"check"`
	Created int64  `json:"created"`
}

// KDF are the Argon2id parameters.
type KDF struct {
	Alg     string `json:"alg"`
	Salt    string `json:"salt"`
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"` // KiB
	Threads uint8  `json:"threads"`
}

// DefaultKDF: 64 MiB, 3 passes (about half a second on a laptop).
var DefaultKDF = KDF{Alg: "argon2id", Time: 3, Memory: 64 << 10, Threads: 4}

var (
	ErrPassphrase  = errors.New("wrong passphrase")
	ErrRecoveryKey = errors.New("wrong recovery key")
	ErrCorrupt     = errors.New("encrypted data altered or damaged")
	ErrName        = errors.New("not an encrypted name of this folder")
)

// KeySize is the size of the master key.
const KeySize = 32

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func passKey(pass string, k KDF) ([]byte, error) {
	salt, err := base64.StdEncoding.DecodeString(k.Salt)
	if err != nil || len(salt) < 16 || k.Alg != "argon2id" || k.Time < 1 || k.Memory < 16<<10 || k.Memory > 4<<20 || k.Threads < 1 {
		return nil, errors.New("unsupported key derivation parameters")
	}
	return argon2.IDKey([]byte(pass), salt, k.Time, k.Memory, k.Threads, 32), nil
}

func keyAD(id string) []byte { return []byte("strataos-e2e v1 key " + id) }

func check(mk []byte, id string) string {
	m := hmac.New(sha256.New, mk)
	m.Write([]byte("strataos-e2e v1 check " + id))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func wrap(mk []byte, pass string, h *Header) error {
	kek, err := passKey(pass, h.KDF)
	if err != nil {
		return err
	}
	a, _ := chacha20poly1305.NewX(kek)
	nonce := random(a.NonceSize())
	h.Wrapped = base64.StdEncoding.EncodeToString(append(nonce, a.Seal(nil, nonce, mk, keyAD(h.ID))...))
	return nil
}

// MinPassphrase is the shortest passphrase accepted.
const MinPassphrase = 10

// New makes the header of a new encrypted folder and returns its master key.
func New(pass string) (Header, []byte, error) {
	if len([]rune(pass)) < MinPassphrase {
		return Header{}, nil, fmt.Errorf("the passphrase must have at least %d characters", MinPassphrase)
	}
	mk := random(KeySize)
	h := Header{V: 1, ID: hex.EncodeToString(random(16)), Cipher: "xchacha20poly1305", KDF: DefaultKDF, Created: time.Now().UnixMilli()}
	h.KDF.Salt = base64.StdEncoding.EncodeToString(random(16))
	h.Check = check(mk, h.ID)
	if err := wrap(mk, pass, &h); err != nil {
		return Header{}, nil, err
	}
	return h, mk, nil
}

// Unlock returns the master key of a folder from its passphrase.
func Unlock(h Header, pass string) ([]byte, error) {
	if h.V != 1 || h.Cipher != "xchacha20poly1305" {
		return nil, errors.New("this encrypted folder needs a newer version of the app")
	}
	kek, err := passKey(pass, h.KDF)
	if err != nil {
		return nil, err
	}
	w, err := base64.StdEncoding.DecodeString(h.Wrapped)
	a, _ := chacha20poly1305.NewX(kek)
	if err != nil || len(w) < a.NonceSize() {
		return nil, ErrCorrupt
	}
	mk, err := a.Open(nil, w[:a.NonceSize()], w[a.NonceSize():], keyAD(h.ID))
	if err != nil {
		return nil, ErrPassphrase
	}
	if err := Verify(h, mk); err != nil {
		return nil, err
	}
	return mk, nil
}

// Verify checks that mk is the key of the folder.
func Verify(h Header, mk []byte) error {
	if len(mk) != KeySize || !hmac.Equal([]byte(check(mk, h.ID)), []byte(h.Check)) {
		return ErrRecoveryKey
	}
	return nil
}

// Rewrap returns the header with the key sealed by a new passphrase.
func Rewrap(h Header, mk []byte, pass string) (Header, error) {
	if len([]rune(pass)) < MinPassphrase {
		return Header{}, fmt.Errorf("the passphrase must have at least %d characters", MinPassphrase)
	}
	if err := Verify(h, mk); err != nil {
		return Header{}, err
	}
	h.KDF = DefaultKDF
	h.KDF.Salt = base64.StdEncoding.EncodeToString(random(16))
	if err := wrap(mk, pass, &h); err != nil {
		return Header{}, err
	}
	return h, nil
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// RecoveryKey writes the master key for a person: base32, a 2-byte
// checksum, groups of 4 characters.
func RecoveryKey(mk []byte) string {
	sum := sha256.Sum256(mk)
	s := b32.EncodeToString(append(append([]byte{}, mk...), sum[:2]...))
	var parts []string
	for len(s) > 4 {
		parts = append(parts, s[:4])
		s = s[4:]
	}
	return strings.Join(append(parts, s), "-")
}

// ParseRecoveryKey reads a recovery key (spaces, dashes and case ignored).
func ParseRecoveryKey(s string) ([]byte, error) {
	s = strings.ToUpper(strings.NewReplacer("-", "", " ", "", "\n", "", "\r", "", "\t", "").Replace(s))
	b, err := b32.DecodeString(s)
	if err != nil || len(b) != KeySize+2 {
		return nil, ErrRecoveryKey
	}
	sum := sha256.Sum256(b[:KeySize])
	if !bytes.Equal(sum[:2], b[KeySize:]) {
		return nil, ErrRecoveryKey
	}
	return b[:KeySize], nil
}

// Keys are the keys of a folder, derived from its master key.
type Keys struct {
	content []byte
	nameEnc []byte
	nameMAC []byte
}

// Derive returns the working keys of a master key.
func Derive(mk []byte) (*Keys, error) {
	if len(mk) != KeySize {
		return nil, errors.New("bad key size")
	}
	k := &Keys{}
	for _, x := range []struct {
		dst  *[]byte
		info string
	}{{&k.content, "content"}, {&k.nameEnc, "name-enc"}, {&k.nameMAC, "name-mac"}} {
		*x.dst = make([]byte, 32)
		if _, err := io.ReadFull(hkdf.New(sha256.New, mk, nil, []byte("strataos-e2e v1 "+x.info)), *x.dst); err != nil {
			return nil, err
		}
	}
	return k, nil
}

// ---- Names ----

// MaxName is the longest plaintext name (bytes) whose encrypted form fits
// the 255-byte limit of the NAS.
const MaxName = 255*3/4 - 24 - 16

var b64 = base64.RawURLEncoding

// EncryptName encrypts one name (no slash). The same name always gives the
// same result, so the client can find a file by its path.
func (k *Keys) EncryptName(name string) (string, error) {
	if len(name) > MaxName {
		return "", fmt.Errorf("name too long for an encrypted folder (%d bytes at most): %s", MaxName, name)
	}
	m := hmac.New(sha256.New, k.nameMAC)
	m.Write([]byte(name))
	nonce := m.Sum(nil)[:chacha20poly1305.NonceSizeX]
	a, _ := chacha20poly1305.NewX(k.nameEnc)
	return b64.EncodeToString(append(nonce, a.Seal(nil, nonce, []byte(name), []byte("name"))...)), nil
}

// DecryptName decrypts one name.
func (k *Keys) DecryptName(enc string) (string, error) {
	b, err := b64.DecodeString(enc)
	if err != nil || len(b) < chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead {
		return "", ErrName
	}
	a, _ := chacha20poly1305.NewX(k.nameEnc)
	nonce := b[:chacha20poly1305.NonceSizeX]
	p, err := a.Open(nil, nonce, b[chacha20poly1305.NonceSizeX:], []byte("name"))
	if err != nil {
		return "", ErrName
	}
	m := hmac.New(sha256.New, k.nameMAC)
	m.Write(p)
	if !hmac.Equal(m.Sum(nil)[:chacha20poly1305.NonceSizeX], nonce) {
		return "", ErrName
	}
	return string(p), nil
}

// EncryptPath encrypts each component of a slash path.
func (k *Keys) EncryptPath(rel string) (string, error) {
	if rel == "" {
		return "", nil
	}
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		e, err := k.EncryptName(p)
		if err != nil {
			return "", err
		}
		parts[i] = e
	}
	return strings.Join(parts, "/"), nil
}

// DecryptPath decrypts each component of a slash path.
func (k *Keys) DecryptPath(rel string) (string, error) {
	if rel == "" {
		return "", nil
	}
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		d, err := k.DecryptName(p)
		if err != nil {
			return "", err
		}
		if d == "" || d == "." || d == ".." || strings.ContainsAny(d, "/\\\x00") {
			return "", ErrName
		}
		parts[i] = d
	}
	return strings.Join(parts, "/"), nil
}

// ---- Contents ----

const (
	// ChunkSize is the plaintext size of a chunk.
	ChunkSize = 64 << 10
	magic     = "SE2E"
	hdrLen    = 4 + 1 + 16
	metaLen   = 8 + 32 + chacha20poly1305.Overhead
	// HeadLen is the fixed part before the first chunk.
	HeadLen  = hdrLen + metaLen
	sealedCk = ChunkSize + chacha20poly1305.Overhead
)

func chunks(plain int64) int64 {
	if plain == 0 {
		return 1
	}
	return (plain + ChunkSize - 1) / ChunkSize
}

// CipherSize is the size of the encrypted form of plain bytes.
func CipherSize(plain int64) int64 {
	return HeadLen + plain + chunks(plain)*chacha20poly1305.Overhead
}

// PlainSize is the size of the plaintext of an encrypted file of c bytes.
func PlainSize(c int64) (int64, error) {
	body := c - HeadLen
	if body < chacha20poly1305.Overhead {
		return 0, ErrCorrupt
	}
	n := (body + sealedCk - 1) / sealedCk
	p := body - n*chacha20poly1305.Overhead
	if p < 0 || CipherSize(p) != c {
		return 0, ErrCorrupt
	}
	return p, nil
}

func nonceFor(prefix []byte, i uint64) []byte {
	n := make([]byte, chacha20poly1305.NonceSizeX)
	copy(n, prefix)
	binary.BigEndian.PutUint64(n[16:], i)
	return n
}

func chunkAD(hdr []byte, last bool) []byte {
	ad := append([]byte{}, hdr...)
	if last {
		return append(ad, 1)
	}
	return append(ad, 0)
}

const metaIndex = ^uint64(0)

// Encrypter is the encrypted form of a plaintext, read at any offset (what
// uploads need). The plaintext must not change while it is read: its size
// and SHA-256 are in the metadata.
type Encrypter struct {
	k      *Keys
	src    io.ReaderAt
	size   int64
	head   []byte // header and metadata
	hdr    []byte
	prefix []byte
	// last chunk encrypted
	ci  int64
	ct  []byte
	buf []byte
}

// NewEncrypter reads the plaintext once (its SHA-256) and returns the
// encrypted view of it.
func (k *Keys) NewEncrypter(src io.ReaderAt, size int64) (*Encrypter, error) {
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(src, 0, size)); err != nil {
		return nil, err
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return k.newEncrypter(src, size, sum), nil
}

func (k *Keys) newEncrypter(src io.ReaderAt, size int64, sum [32]byte) *Encrypter {
	e := &Encrypter{k: k, src: src, size: size, prefix: random(16), ci: -1}
	e.hdr = append(append([]byte(magic), 1), e.prefix...)
	meta := make([]byte, 8, 40)
	binary.BigEndian.PutUint64(meta, uint64(size))
	meta = append(meta, sum[:]...)
	a, _ := chacha20poly1305.NewX(k.content)
	e.head = append(append([]byte{}, e.hdr...), a.Seal(nil, nonceFor(e.prefix, metaIndex), meta, e.hdr)...)
	return e
}

// Size is the size of the encrypted form.
func (e *Encrypter) Size() int64 { return CipherSize(e.size) }

func (e *Encrypter) chunk(i int64) ([]byte, error) {
	if i == e.ci {
		return e.ct, nil
	}
	n := chunks(e.size)
	start := i * ChunkSize
	l := min(int64(ChunkSize), e.size-start)
	if cap(e.buf) < ChunkSize {
		e.buf = make([]byte, ChunkSize)
	}
	p := e.buf[:l]
	if l > 0 {
		if _, err := e.src.ReadAt(p, start); err != nil && !(errors.Is(err, io.EOF)) {
			return nil, err
		}
	}
	a, _ := chacha20poly1305.NewX(e.k.content)
	e.ct = a.Seal(e.ct[:0], nonceFor(e.prefix, uint64(i)), p, chunkAD(e.hdr, i == n-1))
	e.ci = i
	return e.ct, nil
}

// ReadAt reads the encrypted form.
func (e *Encrypter) ReadAt(p []byte, off int64) (int, error) {
	total := e.Size()
	n := 0
	for n < len(p) && off < total {
		if off < HeadLen {
			c := copy(p[n:], e.head[off:])
			n += c
			off += int64(c)
			continue
		}
		i := (off - HeadLen) / sealedCk
		ct, err := e.chunk(i)
		if err != nil {
			return n, err
		}
		c := copy(p[n:], ct[(off-HeadLen)-i*sealedCk:])
		n += c
		off += int64(c)
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// Meta is what the metadata of an encrypted file says.
type Meta struct {
	Size   int64
	SHA256 string
}

// ReadHead decrypts the header and metadata (the first HeadLen bytes).
func (k *Keys) ReadHead(head []byte) (Meta, []byte, error) {
	if len(head) < HeadLen || string(head[:4]) != magic {
		return Meta{}, nil, ErrCorrupt
	}
	if head[4] != 1 {
		return Meta{}, nil, errors.New("this file needs a newer version of the app")
	}
	hdr := head[:hdrLen]
	a, _ := chacha20poly1305.NewX(k.content)
	m, err := a.Open(nil, nonceFor(hdr[5:], metaIndex), head[hdrLen:HeadLen], hdr)
	if err != nil || len(m) != 40 {
		return Meta{}, nil, ErrCorrupt
	}
	return Meta{Size: int64(binary.BigEndian.Uint64(m)), SHA256: hex.EncodeToString(m[8:])}, append([]byte{}, hdr...), nil
}

// Decrypter turns ciphertext written to it, from the start of chunk first
// (after the header given by ReadHead), into plaintext written to out,
// skipping the first skip bytes of plaintext.
type Decrypter struct {
	k     *Keys
	hdr   []byte
	meta  Meta
	next  int64
	last  int64
	skip  int64
	out   io.Writer
	buf   []byte
	hash  io.Writer
	sum   func() []byte
	n     int64 // plaintext bytes written to out
	whole bool
}

// NewDecrypter starts decrypting at chunk first. With first 0 and skip 0 it
// also checks the SHA-256 of the whole file at Finish.
func (k *Keys) NewDecrypter(hdr []byte, meta Meta, first, skip int64, out io.Writer) *Decrypter {
	d := &Decrypter{k: k, hdr: hdr, meta: meta, next: first, last: chunks(meta.Size) - 1, skip: skip, out: out}
	if first == 0 && skip == 0 {
		h := sha256.New()
		d.hash, d.sum, d.whole = h, func() []byte { return h.Sum(nil) }, true
	}
	return d
}

// Written is the number of plaintext bytes written to out.
func (d *Decrypter) Written() int64 { return d.n }

func (d *Decrypter) open(ct []byte) error {
	if d.next > d.last {
		return ErrCorrupt // data after the last chunk
	}
	a, _ := chacha20poly1305.NewX(d.k.content)
	p, err := a.Open(nil, nonceFor(d.hdr[5:], uint64(d.next)), ct, chunkAD(d.hdr, d.next == d.last))
	if err != nil {
		return ErrCorrupt
	}
	want := int64(ChunkSize)
	if d.next == d.last {
		want = d.meta.Size - d.last*ChunkSize
	}
	if int64(len(p)) != want {
		return ErrCorrupt
	}
	d.next++
	if d.hash != nil {
		d.hash.Write(p)
	}
	if d.skip > 0 {
		s := min(d.skip, int64(len(p)))
		p, d.skip = p[s:], d.skip-s
	}
	if len(p) > 0 {
		if _, err := d.out.Write(p); err != nil {
			return err
		}
		d.n += int64(len(p))
	}
	return nil
}

func (d *Decrypter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		c := min(len(p), sealedCk-len(d.buf))
		d.buf = append(d.buf, p[:c]...)
		p = p[c:]
		if len(d.buf) == sealedCk {
			if err := d.open(d.buf); err != nil {
				return 0, err
			}
			d.buf = d.buf[:0]
		}
	}
	return n, nil
}

// Finish checks that the file ended where it should (and its SHA-256 when
// it was read whole).
func (d *Decrypter) Finish() error {
	if len(d.buf) > 0 {
		if err := d.open(d.buf); err != nil {
			return err
		}
		d.buf = d.buf[:0]
	}
	if d.next != d.last+1 {
		return ErrCorrupt // truncated
	}
	if d.whole && hex.EncodeToString(d.sum()) != d.meta.SHA256 {
		return errors.New("the file changed while it was encrypted: it will be sent again")
	}
	return nil
}

// ChunkOffset is where chunk i starts in the encrypted file.
func ChunkOffset(i int64) int64 { return HeadLen + i*sealedCk }
