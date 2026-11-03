package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testE2EHeader() e2eHeaderDoc {
	b := func(n int) string { return base64.StdEncoding.EncodeToString(make([]byte, n)) }
	return e2eHeaderDoc{V: 1, ID: strings.Repeat("ab", 16), Cipher: "xchacha20poly1305",
		KDF: e2eKDF{Alg: "argon2id", Salt: b(16), Time: 3, Memory: 64 << 10, Threads: 4}, Wrapped: b(72), Check: b(32), Created: 1}
}

func TestE2EHeaderValidate(t *testing.T) {
	h := testE2EHeader()
	if err := h.validate(); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]func(*e2eHeaderDoc){
		"version": func(h *e2eHeaderDoc) { h.V = 2 },
		"id":      func(h *e2eHeaderDoc) { h.ID = "../x" },
		"cipher":  func(h *e2eHeaderDoc) { h.Cipher = "none" },
		"kdf":     func(h *e2eHeaderDoc) { h.KDF.Alg = "pbkdf2" },
		"memory":  func(h *e2eHeaderDoc) { h.KDF.Memory = 1 << 30 },
		"salt":    func(h *e2eHeaderDoc) { h.KDF.Salt = "AA==" },
		"wrapped": func(h *e2eHeaderDoc) { h.Wrapped = "not base64!" },
		"check":   func(h *e2eHeaderDoc) { h.Check = "" },
	} {
		x := testE2EHeader()
		f(&x)
		if x.validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestE2EFolderReadOnly: inside an encrypted folder, the file manager is
// read-only; a sync client naming the key ID gets the user's right back;
// the folder itself may still be deleted or moved as a whole.
func TestE2EFolderReadOnly(t *testing.T) {
	top := t.TempDir()
	old := e2eTop
	e2eTop = top
	defer func() { e2eTop = old }()
	enc := filepath.Join(top, "share", "secret")
	os.MkdirAll(filepath.Join(enc, "sub"), 0o755)
	os.MkdirAll(filepath.Join(top, "share", "plain"), 0o755)
	h := testE2EHeader()
	b, _ := json.Marshal(h)
	os.WriteFile(filepath.Join(enc, e2eMarker), b, 0o644)

	if got := e2eRootOf(filepath.Join(enc, "sub", "f.bin")); got != enc {
		t.Errorf("root of a file inside: %q", got)
	}
	if got := e2eRootOf(filepath.Join(top, "share", "plain")); got != "" {
		t.Errorf("plain folder seen encrypted: %q", got)
	}
	// A symlink is not a header.
	os.Symlink(filepath.Join(enc, e2eMarker), filepath.Join(top, "share", "plain", e2eMarker))
	if got := e2eRootOf(filepath.Join(top, "share", "plain")); got != "" {
		t.Errorf("symlinked header followed: %q", got)
	}

	in := e2eTarget(target{abs: filepath.Join(enc, "sub"), acc: access{read: true, write: true}})
	if in.acc.write || !in.acc.e2eWrite || in.acc.e2e != enc || in.removable() {
		t.Errorf("inside: %+v", in.acc)
	}
	rootT := e2eTarget(target{abs: enc, acc: access{read: true, write: true}})
	if rootT.acc.write || !rootT.removable() {
		t.Errorf("encrypted folder itself: %+v removable %v", rootT.acc, rootT.removable())
	}
	ro := e2eTarget(target{abs: enc, acc: access{read: true}})
	if ro.removable() {
		t.Error("read-only user may delete the encrypted folder")
	}

	s := newTestServer(t)
	r := httptest.NewRequest("GET", "/", nil)
	x := in
	s.e2eAllow(r, &x)
	if x.acc.write {
		t.Error("write given back without the key ID")
	}
	r.Header.Set(e2eHeader, strings.Repeat("cd", 16))
	s.e2eAllow(r, &x)
	if x.acc.write {
		t.Error("write given back with another key ID")
	}
	r.Header.Set(e2eHeader, h.ID)
	s.e2eAllow(r, &x)
	if !x.acc.write {
		t.Error("write not given back to the encrypting client")
	}
	if err := s.e2eClient(r, enc, filepath.Join(enc, "sub")); err == nil {
		t.Error("a subfolder of an encrypted folder followed")
	}
	if err := s.e2eClient(r, enc, enc); err != nil {
		t.Error(err)
	}
	r.Header.Del(e2eHeader)
	if err := s.e2eClient(r, enc, enc); err == nil || !strings.Contains(err.Error(), "end-to-end encrypted") {
		t.Errorf("plain client: %v", err)
	}
}
