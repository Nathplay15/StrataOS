package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func mobileTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := newTestServer(t)
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "alice", Role: "user"}, &store.User{Username: "root1", Role: "admin"})
		return nil
	})
	tok, _, err := s.addDevice("alice", "Pixel", "Google Pixel 8", "0.7.0", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	s.user("GET /api/mobile/test", func(r *http.Request) (any, error) {
		if d := deviceFrom(r.Context()); d != nil {
			return map[string]string{"user": userOf(r), "device": d.id}, nil
		}
		return map[string]string{"user": userOf(r)}, nil
	})
	s.user("POST /api/mobile/test", func(r *http.Request) (any, error) { return nil, nil })
	s.user("POST /api/me/devices/pairing", func(r *http.Request) (any, error) { return nil, nil })
	s.admin("GET /api/admin-only", func(r *http.Request) (any, error) { return nil, nil })
	return s, tok
}

func doReq(s *Server, method, path, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "192.0.2.10:5555"
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	return w
}

func TestDeviceTokenAuth(t *testing.T) {
	s, tok := mobileTestServer(t)
	if !strings.HasPrefix(tok, deviceTokenPrefix) || len(tok) != len(deviceTokenPrefix)+64 {
		t.Fatalf("token %q", tok)
	}
	w := doReq(s, "GET", "/api/mobile/test", tok)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"user":"alice"`) || !strings.Contains(w.Body.String(), `"device"`) {
		t.Fatalf("valid token: %d %s", w.Code, w.Body)
	}
	// No Origin, Referer or X-Strata: bearer requests are not CSRF-prone.
	if w := doReq(s, "POST", "/api/mobile/test", tok); w.Code != 200 {
		t.Fatalf("POST with token: %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{"stdev_" + strings.Repeat("0", 64), "nope", tok + "x"} {
		if w := doReq(s, "GET", "/api/mobile/test", bad); w.Code != 401 {
			t.Errorf("token %q: %d", bad, w.Code)
		}
	}
	if w := doReq(s, "GET", "/api/admin-only", tok); w.Code != 403 {
		t.Errorf("user token on an admin route: %d", w.Code)
	}
	if w := doReq(s, "POST", "/api/me/devices/pairing", tok); w.Code != 403 {
		t.Errorf("token creating a pairing code: %d", w.Code)
	}
	// The role is the user's current one.
	_ = s.store.Update(func(st *store.State) error { st.User("alice").Role = "admin"; return nil })
	if w := doReq(s, "GET", "/api/admin-only", tok); w.Code != 200 {
		t.Errorf("promoted user: %d", w.Code)
	}
	_ = s.store.Update(func(st *store.State) error { st.User("alice").Disabled = true; return nil })
	if w := doReq(s, "GET", "/api/mobile/test", tok); w.Code != 401 {
		t.Errorf("disabled user: %d", w.Code)
	}
	_ = s.store.Update(func(st *store.State) error { st.User("alice").Disabled = false; return nil })
	var id string
	s.store.Read(func(st *store.State) {
		d := st.User("alice").MobileDevices[0]
		id = d.ID
		h := sha256.Sum256([]byte(tok))
		if d.TokenHash != hex.EncodeToString(h[:]) || d.LastIP != "192.0.2.10" {
			t.Errorf("stored device %+v", d)
		}
	})
	if err := s.removeDevice("alice", id, "revoked "); err != nil {
		t.Fatal(err)
	}
	if w := doReq(s, "GET", "/api/mobile/test", tok); w.Code != 401 {
		t.Errorf("revoked token: %d", w.Code)
	}
}

func TestDeviceLimitAndLabels(t *testing.T) {
	s, _ := mobileTestServer(t)
	for i := 1; i < maxDevicesPerUser; i++ {
		if _, _, err := s.addDevice("alice", "", "", "", ""); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, _, err := s.addDevice("alice", "x", "", "", ""); err == nil {
		t.Fatal("device limit not enforced")
	}
	if _, _, err := s.addDevice("nobody", "x", "", "", ""); err == nil {
		t.Fatal("device for an unknown user")
	}
	if got := cleanLabel("  Pixel\n\x00 de Bob ", 64); got != "Pixel de Bob" {
		t.Errorf("cleanLabel %q", got)
	}
	if got := cleanLabel(strings.Repeat("é", 70), 64); got != strings.Repeat("é", 64) {
		t.Errorf("cleanLabel length %d", len(got))
	}
}

func TestPairingCodes(t *testing.T) {
	p := &devicePairings{m: map[string]devicePairing{}}
	c := p.create("alice")
	if len(c) != 32 {
		t.Fatalf("code %q", c)
	}
	if p.take("wrong") != "" {
		t.Fatal("wrong code accepted")
	}
	if p.take(strings.ToUpper(c)) != "alice" || p.take(c) != "" {
		t.Fatal("code not single-use")
	}
	c1 := p.create("bob")
	c2 := p.create("bob")
	if p.take(c1) != "" || p.take(c2) != "bob" {
		t.Fatal("older code of the same user still valid")
	}
	c3 := p.create("carol")
	for k, v := range p.m {
		v.expires = time.Now().Add(-time.Second)
		p.m[k] = v
	}
	if p.take(c3) != "" {
		t.Fatal("expired code accepted")
	}
}

func TestDeviceDenied(t *testing.T) {
	for p, want := range map[string]bool{"/api/me/devices": true, "/api/me/devices/pairing": true, "/api/me/password": true, "/api/me/2fa/disable": true,
		"/api/me/passkeys": true, "/api/logout": true, "/api/me/quotas": false, "/api/me": false, "/api/files": false, "/api/mobile/device": false} {
		if deviceDenied(p) != want {
			t.Errorf("%s: %v", p, !want)
		}
	}
}

func TestValidFileName(t *testing.T) {
	for n, ok := range map[string]bool{"IMG_0001.jpg": true, "été 2024.mp4": true, "": false, ".": false, "..": false, "a/b": false, `a\b`: false,
		".strata-upload-x": false, ".hidden": false, "a\x00b": false, "a\nb": false, strings.Repeat("a", 256): false} {
		if (validFileName(n) == nil) != ok {
			t.Errorf("%q: %v", n, !ok)
		}
	}
	for _, u := range []string{"https://ntfy.sh/up123?up=1", "http://192.168.1.5:8080/up"} {
		if validPushURL(u) != nil {
			t.Errorf("%s refused", u)
		}
	}
	for _, u := range []string{"ftp://x/y", "https://", "file:///etc/passwd", "https://user:pw@ntfy.sh/x", "javascript:alert(1)"} {
		if validPushURL(u) == nil {
			t.Errorf("%s accepted", u)
		}
	}
}

func writeTemp(t *testing.T, rt *os.Root, rel, data string) {
	t.Helper()
	if err := rt.WriteFile(rel, []byte(data), 0o664); err != nil {
		t.Fatal(err)
	}
}

func TestFinishUpload(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "DCIM"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	mt := time.Date(2021, 7, 14, 10, 30, 0, 0, time.UTC)
	sum := sha256.Sum256([]byte("hello"))
	u := &mobileUpload{ID: "a1", Name: "IMG_1.jpg", Size: 5, ModTime: mt.UnixMilli(), SHA256: hex.EncodeToString(sum[:])}

	// Incomplete: refused, the temp file stays for a resume.
	writeTemp(t, rt, "DCIM/"+u.temp(), "hel")
	if _, _, err := finishUpload(rt, "DCIM", u, nil); err == nil {
		t.Fatal("incomplete upload finished")
	}
	writeTemp(t, rt, "DCIM/"+u.temp(), "hello")
	owned := ""
	name, dup, err := finishUpload(rt, "DCIM", u, func(rel string) { owned = rel })
	if err != nil || name != "IMG_1.jpg" || dup || owned != "DCIM/"+u.temp() {
		t.Fatalf("finish: %q %v %v %q", name, dup, err, owned)
	}
	st, err := os.Stat(filepath.Join(dir, "DCIM/IMG_1.jpg"))
	if err != nil || !st.ModTime().Equal(mt) {
		t.Fatalf("date not kept: %v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "DCIM", u.temp())); !os.IsNotExist(err) {
		t.Fatal("temp file left")
	}

	// Same file again: a duplicate, nothing added.
	writeTemp(t, rt, "DCIM/"+u.temp(), "hello")
	if name, dup, err := finishUpload(rt, "DCIM", u, nil); err != nil || !dup || name != "IMG_1.jpg" {
		t.Fatalf("duplicate: %q %v %v", name, dup, err)
	}
	// Another file with that name: kept side by side, never overwritten.
	u2 := &mobileUpload{ID: "a2", Name: "IMG_1.jpg", Size: 6}
	writeTemp(t, rt, "DCIM/"+u2.temp(), "hello!")
	if name, dup, err := finishUpload(rt, "DCIM", u2, nil); err != nil || dup || name != "IMG_1 (2).jpg" {
		t.Fatalf("conflict: %q %v %v", name, dup, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "DCIM/IMG_1.jpg")); string(b) != "hello" {
		t.Fatal("original overwritten")
	}
	// Damaged in transit: refused and removed.
	u3 := &mobileUpload{ID: "a3", Name: "x.bin", Size: 5, SHA256: u.SHA256}
	writeTemp(t, rt, "DCIM/"+u3.temp(), "HELLO")
	if _, _, err := finishUpload(rt, "DCIM", u3, nil); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("checksum: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "DCIM", u3.temp())); !os.IsNotExist(err) {
		t.Fatal("damaged temp file left")
	}
	if !sameFile(rt, "DCIM/IMG_1.jpg", 5, mt.UnixMilli()) || sameFile(rt, "DCIM/IMG_1.jpg", 5, mt.Add(time.Hour).UnixMilli()) || sameFile(rt, "DCIM/IMG_1.jpg", 4, 0) {
		t.Fatal("sameFile")
	}
}

func TestMobileFeed(t *testing.T) {
	s := newTestServer(t)
	feed = &mobileFeed{}
	defer func() { feed = &mobileFeed{} }()
	for i := range 5 {
		s.mobileFeedAdd([]string{"info", "warning", "error", "", "info"}[i], "Subject", "msg")
	}
	ev, cur := s.mobileFeedSince(0, 50)
	if len(ev) != 5 || cur != 5 || ev[3].Level != "info" {
		t.Fatalf("all: %d %d", len(ev), cur)
	}
	if ev, cur := s.mobileFeedSince(3, 50); len(ev) != 2 || ev[0].ID != 4 || cur != 5 {
		t.Fatalf("since 3: %v %d", ev, cur)
	}
	if ev, _ := s.mobileFeedSince(0, 2); len(ev) != 2 || ev[1].ID != 5 {
		t.Fatalf("limit: %v", ev)
	}
	// A cursor from the future (feed reset by a restore): start over.
	if ev, _ := s.mobileFeedSince(99, 50); len(ev) != 5 {
		t.Fatalf("reset: %v", ev)
	}
	// Persisted across restarts.
	feed = &mobileFeed{}
	if ev, cur := s.mobileFeedSince(4, 50); len(ev) != 1 || cur != 5 {
		t.Fatalf("reloaded: %v %d", ev, cur)
	}
}
