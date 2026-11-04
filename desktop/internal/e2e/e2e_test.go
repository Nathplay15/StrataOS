package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

func init() {
	// Fast key derivation for tests (the minimum the format accepts).
	DefaultKDF = KDF{Alg: "argon2id", Time: 1, Memory: 16 << 10, Threads: 1}
}

func testKeys(t *testing.T) *Keys {
	t.Helper()
	_, mk, err := New("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	k, err := Derive(mk)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestPassphraseAndRecovery(t *testing.T) {
	h, mk, err := New("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := New("short"); err == nil {
		t.Error("short passphrase accepted")
	}
	got, err := Unlock(h, "correct horse battery")
	if err != nil || !bytes.Equal(got, mk) {
		t.Fatalf("unlock: %v", err)
	}
	if _, err := Unlock(h, "wrong horse battery"); !errors.Is(err, ErrPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
	rk := RecoveryKey(mk)
	back, err := ParseRecoveryKey(strings.ToLower(strings.ReplaceAll(rk, "-", " ")))
	if err != nil || !bytes.Equal(back, mk) || Verify(h, back) != nil {
		t.Fatalf("recovery key %s: %v", rk, err)
	}
	typo := []byte(rk)
	if typo[0] == 'A' {
		typo[0] = 'B'
	} else {
		typo[0] = 'A'
	}
	if _, err := ParseRecoveryKey(string(typo)); err == nil {
		t.Error("recovery key with a typo accepted")
	}
	other := make([]byte, KeySize)
	if Verify(h, other) == nil {
		t.Error("another key verified")
	}
	// A new passphrase keeps the key and the folder ID.
	h2, err := Rewrap(h, mk, "a brand new passphrase")
	if err != nil || h2.ID != h.ID {
		t.Fatal(err)
	}
	if got, err := Unlock(h2, "a brand new passphrase"); err != nil || !bytes.Equal(got, mk) {
		t.Errorf("unlock after rewrap: %v", err)
	}
	if _, err := Unlock(h2, "correct horse battery"); err == nil {
		t.Error("old passphrase still works")
	}
	// A header altered on the NAS (another folder's ID) does not unlock.
	h3 := h
	h3.ID = strings.Repeat("0", 32)
	if _, err := Unlock(h3, "correct horse battery"); err == nil {
		t.Error("header with another ID unlocked")
	}
}

func TestNames(t *testing.T) {
	k := testKeys(t)
	for _, n := range []string{"a.txt", "Été 2024 — photos", ".hidden", strings.Repeat("x", MaxName), "日本語.pdf"} {
		e, err := k.EncryptName(n)
		if err != nil {
			t.Fatal(err)
		}
		if len(e) > 255 || strings.ContainsAny(e, "/\\.") || strings.Contains(e, n) {
			t.Errorf("encrypted name %q of %q", e, n)
		}
		e2, _ := k.EncryptName(n)
		if e != e2 {
			t.Error("names are not deterministic")
		}
		d, err := k.DecryptName(e)
		if err != nil || d != n {
			t.Errorf("round trip of %q: %q %v", n, d, err)
		}
	}
	if _, err := k.EncryptName(strings.Repeat("x", MaxName+1)); err == nil {
		t.Error("overlong name accepted")
	}
	e, _ := k.EncryptName("report.pdf")
	b := []byte(e)
	b[len(b)/2] ^= 1
	if _, err := k.DecryptName(string(b)); err == nil {
		t.Error("altered name decrypted")
	}
	if _, err := k.DecryptName("plain-name.txt"); err == nil {
		t.Error("plaintext name decrypted")
	}
	other := testKeys(t)
	if _, err := other.DecryptName(e); err == nil {
		t.Error("name decrypted with another folder's key")
	}
	p, _ := k.EncryptPath("a/b/c.txt")
	if strings.Count(p, "/") != 2 {
		t.Errorf("path %q", p)
	}
	if d, err := k.DecryptPath(p); err != nil || d != "a/b/c.txt" {
		t.Errorf("path round trip: %q %v", d, err)
	}
}

func encrypt(t *testing.T, k *Keys, plain []byte) []byte {
	t.Helper()
	e, err := k.NewEncrypter(bytes.NewReader(plain), int64(len(plain)))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := io.ReadAll(io.NewSectionReader(e, 0, e.Size()))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(ct)) != CipherSize(int64(len(plain))) {
		t.Fatalf("size %d, want %d", len(ct), CipherSize(int64(len(plain))))
	}
	return ct
}

func decrypt(k *Keys, ct []byte, first, skip int64) ([]byte, error) {
	m, hdr, err := k.ReadHead(ct)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	d := k.NewDecrypter(hdr, m, first, skip, &out)
	start := ChunkOffset(first)
	if start > int64(len(ct)) {
		return nil, ErrCorrupt
	}
	// Odd write sizes, like a network stream.
	rest := ct[start:]
	for len(rest) > 0 {
		n := min(len(rest), 7777)
		if _, err := d.Write(rest[:n]); err != nil {
			return nil, err
		}
		rest = rest[n:]
	}
	err = d.Finish()
	return out.Bytes(), err
}

func TestContentRoundTrip(t *testing.T) {
	k := testKeys(t)
	for _, size := range []int{0, 1, 100, ChunkSize - 1, ChunkSize, ChunkSize + 1, 3*ChunkSize + 12345} {
		plain := make([]byte, size)
		rand.Read(plain)
		ct := encrypt(t, k, plain)
		if p, err := PlainSize(int64(len(ct))); err != nil || p != int64(size) {
			t.Errorf("PlainSize(%d) = %d, %v; want %d", len(ct), p, err, size)
		}
		if size > 16 && bytes.Contains(ct, plain[:16]) {
			t.Error("plaintext visible in the ciphertext")
		}
		m, _, err := k.ReadHead(ct)
		sum := sha256.Sum256(plain)
		if err != nil || m.Size != int64(size) || m.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("metadata %+v %v", m, err)
		}
		got, err := decrypt(k, ct, 0, 0)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("size %d: round trip failed: %v", size, err)
		}
		// Resuming in the middle: from the chunk holding the offset.
		if size > ChunkSize {
			off := int64(size) - 300
			got, err := decrypt(k, ct, off/ChunkSize, off%ChunkSize)
			if err != nil || !bytes.Equal(got, plain[off:]) {
				t.Errorf("size %d: resume failed: %v", size, err)
			}
		}
	}
	// Two encryptions of the same content differ (random prefix).
	a, b := encrypt(t, k, []byte("same")), encrypt(t, k, []byte("same"))
	if bytes.Equal(a, b) {
		t.Error("deterministic contents")
	}
}

func TestContentTampering(t *testing.T) {
	k := testKeys(t)
	plain := make([]byte, 2*ChunkSize+100)
	rand.Read(plain)
	ct := encrypt(t, k, plain)
	for name, f := range map[string]func([]byte) []byte{
		"bit flipped in a chunk":  func(c []byte) []byte { c[HeadLen+ChunkSize+5] ^= 1; return c },
		"bit flipped in header":   func(c []byte) []byte { c[7] ^= 1; return c },
		"bit flipped in metadata": func(c []byte) []byte { c[30] ^= 1; return c },
		"truncated at a chunk":    func(c []byte) []byte { return c[:ChunkOffset(2)] },
		"truncated inside":        func(c []byte) []byte { return c[:len(c)-10] },
		"data appended":           func(c []byte) []byte { return append(c, make([]byte, 40)...) },
		"chunks swapped": func(c []byte) []byte {
			x := append([]byte{}, c...)
			copy(x[ChunkOffset(0):ChunkOffset(1)], c[ChunkOffset(1):ChunkOffset(2)])
			copy(x[ChunkOffset(1):ChunkOffset(2)], c[ChunkOffset(0):ChunkOffset(1)])
			return x
		},
	} {
		c := f(append([]byte{}, ct...))
		if _, err := decrypt(k, c, 0, 0); err == nil {
			t.Errorf("%s: not detected", name)
		}
	}
	// A chunk of another file (same key) does not fit.
	other := encrypt(t, k, plain)
	mixed := append([]byte{}, ct...)
	copy(mixed[ChunkOffset(1):ChunkOffset(2)], other[ChunkOffset(1):ChunkOffset(2)])
	if _, err := decrypt(k, mixed, 0, 0); err == nil {
		t.Error("chunk from another file accepted")
	}
	// Another folder's key reads nothing.
	if _, err := decrypt(testKeys(t), ct, 0, 0); err == nil {
		t.Error("decrypted with another key")
	}
}

// A file that changed while it was encrypted is caught by its checksum.
func TestContentChangedDuringUpload(t *testing.T) {
	k := testKeys(t)
	plain := []byte("version one!")
	sum := sha256.Sum256([]byte("version two!"))
	e := k.newEncrypter(bytes.NewReader(plain), int64(len(plain)), sum)
	ct, _ := io.ReadAll(io.NewSectionReader(e, 0, e.Size()))
	if _, err := decrypt(k, ct, 0, 0); err == nil || errors.Is(err, ErrCorrupt) {
		t.Errorf("changed file: %v", err)
	}
}
