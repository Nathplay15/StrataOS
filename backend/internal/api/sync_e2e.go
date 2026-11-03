package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"strataos/internal/storage"
	"strataos/internal/syncjournal"
)

// End-to-end encrypted sync folders. The desktop sync app encrypts names
// and contents before upload (desktop/internal/e2e); the NAS only stores
// ciphertext and a header (.strataos-sync-e2e.json at the root of the
// folder: key derivation parameters and the master key wrapped with the
// passphrase, never the key itself). The NAS keeps plaintext out of such a
// folder: the file manager, uploads and shares links see it read-only, and
// the Sync API only serves clients that name its key ID in X-Strata-E2E
// (other clients get 409 instead of downloading ciphertext or uploading
// plaintext). That header is a guard against mistakes, not a permission:
// the user's rights apply as usual.

const (
	e2eMarker = ".strataos-sync-e2e.json" // ignored by the sync journal (.strataos-sync- prefix)
	e2eHeader = "X-Strata-E2E"
)

// e2eHeaderDoc is the header of an encrypted folder, as the client wrote it.
type e2eHeaderDoc struct {
	V       int    `json:"v"`
	ID      string `json:"id"`     // random, 32 hex: the folder's key ID
	Cipher  string `json:"cipher"` // xchacha20poly1305
	KDF     e2eKDF `json:"kdf"`
	Wrapped string `json:"wrapped"` // master key sealed with the passphrase key (base64)
	Check   string `json:"check"`   // key check value (base64)
	Created int64  `json:"created"` // Unix ms
}

type e2eKDF struct {
	Alg     string `json:"alg"` // argon2id
	Salt    string `json:"salt"`
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"` // KiB
	Threads uint8  `json:"threads"`
}

// e2eTop is where the search for an encrypted folder stops (tests change it).
var e2eTop = storage.MountRoot

var e2eIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func b64len(s string) int {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return -1
	}
	return len(b)
}

func (h *e2eHeaderDoc) validate() error {
	switch {
	case h.V != 1 || !e2eIDRe.MatchString(h.ID) || h.Cipher != "xchacha20poly1305":
		return errors.New("unsupported encrypted folder header")
	case h.KDF.Alg != "argon2id" || b64len(h.KDF.Salt) < 16 || b64len(h.KDF.Salt) > 64:
		return errors.New("bad key derivation parameters")
	case h.KDF.Time < 1 || h.KDF.Time > 20 || h.KDF.Memory < 16<<10 || h.KDF.Memory > 4<<20 || h.KDF.Threads < 1 || h.KDF.Threads > 16:
		return errors.New("bad key derivation parameters")
	case b64len(h.Wrapped) < 32 || b64len(h.Wrapped) > 256 || b64len(h.Check) != 32:
		return errors.New("bad wrapped key")
	}
	return nil
}

// e2eRootOf returns the encrypted folder containing abs (or abs itself),
// "" when there is none.
func e2eRootOf(abs string) string {
	for d := filepath.Clean(abs); strings.HasPrefix(d, e2eTop+"/"); d = filepath.Dir(d) {
		if fi, err := os.Lstat(filepath.Join(d, e2eMarker)); err == nil && fi.Mode().IsRegular() {
			return d
		}
	}
	return ""
}

func readE2EHeader(root string) (*e2eHeaderDoc, error) {
	f, err := os.OpenFile(filepath.Join(root, e2eMarker), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var h e2eHeaderDoc
	if err := json.NewDecoder(io.LimitReader(f, 8<<10)).Decode(&h); err != nil {
		return nil, err
	}
	if err := h.validate(); err != nil {
		return nil, err
	}
	return &h, nil
}

// e2eAllow gives write access back inside an encrypted folder to a client
// that names the folder's key ID (the desktop app, encrypting).
func (s *Server) e2eAllow(r *http.Request, t *target) {
	if t.acc.e2e == "" {
		return
	}
	id := r.Header.Get(e2eHeader)
	if id == "" {
		return
	}
	if h, err := readE2EHeader(t.acc.e2e); err == nil && h.ID == id {
		t.acc.write = t.acc.e2eWrite
	}
}

// e2eClient checks that a sync client follows an encrypted folder as one.
func (s *Server) e2eClient(r *http.Request, root, folder string) error {
	if root != folder {
		return &Error{Status: http.StatusConflict, Msg: "this folder is inside an end-to-end encrypted folder: follow the encrypted folder itself"}
	}
	h, err := readE2EHeader(root)
	if err != nil {
		return &Error{Status: http.StatusConflict, Msg: "unreadable header of the end-to-end encrypted folder"}
	}
	if r.Header.Get(e2eHeader) != h.ID {
		return &Error{Status: http.StatusConflict, Msg: "this folder is end-to-end encrypted: only the StrataOS Sync desktop app with its passphrase can synchronize it"}
	}
	return nil
}

// removable: deleting or moving an encrypted folder as a whole is allowed
// (its content stays read-only to the file manager).
func (t target) removable() bool {
	return t.acc.write || (t.acc.e2e != "" && t.acc.e2e == t.abs && t.acc.e2eWrite)
}

// e2eEmpty tells whether a folder holds nothing but ignored items.
func e2eEmpty(rt *os.Root, rel string) (bool, error) {
	d, err := rt.Open(rel)
	if err != nil {
		return false, err
	}
	defer d.Close()
	ents, err := d.ReadDir(-1)
	if err != nil {
		return false, err
	}
	for _, e := range ents {
		if e.Name() == e2eMarker || junkFile(e.Name()) || syncjournal.Ignored("", e.Name()) {
			continue
		}
		return false, nil
	}
	return true, nil
}

func (s *Server) syncE2ERoutes() {
	// Whether a folder is encrypted, and its header when it is the
	// encrypted folder itself (what a client needs to unlock it).
	s.user("GET /api/sync/e2e", func(r *http.Request) (any, error) {
		t, err := s.locate(r, r.URL.Query().Get("folder"))
		if err != nil {
			return nil, err
		}
		if t.acc.ancestor {
			return map[string]any{"encrypted": false}, nil
		}
		root := e2eRootOf(t.abs)
		if root == "" {
			return map[string]any{"encrypted": false}, nil
		}
		out := map[string]any{"encrypted": true, "root": root}
		if root == t.abs {
			h, err := readE2EHeader(root)
			if err != nil {
				return nil, &Error{Status: http.StatusConflict, Msg: "unreadable header of the end-to-end encrypted folder"}
			}
			out["header"] = h
		}
		return out, nil
	})

	// Marks an empty folder as encrypted (POST), or changes its header
	// (PUT, same key ID: a new passphrase).
	setHeader := func(r *http.Request, replace bool) (any, error) {
		var req struct {
			Folder string       `json:"folder"`
			Header e2eHeaderDoc `json:"header"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := req.Header.validate(); err != nil {
			return nil, badRequest(err.Error())
		}
		t, err := s.locate(r, req.Folder)
		if err != nil {
			return nil, err
		}
		write := t.acc.write || t.acc.e2eWrite
		if !write || t.acc.ancestor {
			return nil, &Error{Status: http.StatusForbidden, Msg: "read-only: you cannot change this folder"}
		}
		if s.ransomBlocked(userOf(r)) {
			return nil, &Error{Status: http.StatusForbidden, Msg: "your account is blocked by the ransomware protection: ask an administrator"}
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		if st, err := rt.Stat(t.rel); err != nil || !st.IsDir() {
			return nil, notFound("folder not found")
		}
		root := e2eRootOf(t.abs)
		b, _ := json.MarshalIndent(req.Header, "", "  ")
		markerRel := path.Join(t.rel, e2eMarker)
		if replace {
			if root != t.abs {
				return nil, badRequest("this folder is not end-to-end encrypted")
			}
			old, err := readE2EHeader(root)
			if err != nil || old.ID != req.Header.ID {
				return nil, &Error{Status: http.StatusPreconditionFailed, Msg: "the folder's key changed: unlock it again"}
			}
			tmp := markerRel + ".tmp"
			if err := rt.WriteFile(tmp, b, 0o644); err != nil {
				return nil, err
			}
			if err := rt.Rename(tmp, markerRel); err != nil {
				return nil, err
			}
			s.audit(r, "files", "Changed the passphrase of the encrypted folder "+t.abs)
			return nil, nil
		}
		if root != "" {
			return nil, &Error{Status: http.StatusConflict, Msg: "this folder is already end-to-end encrypted (or inside an encrypted folder)"}
		}
		if empty, err := e2eEmpty(rt, t.rel); err != nil || !empty {
			return nil, badRequest("only an empty folder can become end-to-end encrypted")
		}
		f, err := rt.OpenFile(markerRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return nil, &Error{Status: http.StatusConflict, Msg: "this folder is already end-to-end encrypted"}
		}
		_, werr := f.Write(b)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = rt.Remove(markerRel)
			return nil, werr
		}
		s.chownForUser(r, rt, markerRel)
		s.audit(r, "files", "Made "+t.abs+" an end-to-end encrypted sync folder")
		return map[string]any{"root": t.abs, "created": time.Now().UnixMilli()}, nil
	}
	s.user("POST /api/sync/e2e", func(r *http.Request) (any, error) { return setHeader(r, false) })
	s.user("PUT /api/sync/e2e", func(r *http.Request) (any, error) { return setHeader(r, true) })

	// Turns an empty encrypted folder back into a plain one.
	s.user("DELETE /api/sync/e2e", func(r *http.Request) (any, error) {
		t, err := s.locate(r, r.URL.Query().Get("folder"))
		if err != nil {
			return nil, err
		}
		if t.acc.e2e != t.abs || !t.acc.e2eWrite {
			return nil, badRequest("this folder is not end-to-end encrypted, or read-only")
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		if empty, err := e2eEmpty(rt, t.rel); err != nil || !empty {
			return nil, badRequest("empty the folder first: its files can only be read with the passphrase")
		}
		if err := rt.Remove(path.Join(t.rel, e2eMarker)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		s.audit(r, "files", "Turned off end-to-end encryption of "+t.abs)
		return nil, nil
	})
}
