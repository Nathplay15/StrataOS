package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"strataos/internal/jwt"
)

func TestOfficeSignedURLs(t *testing.T) {
	s := newTestServer(t)
	now := time.Now()
	tok, err := s.signOffice(officeClaims{Use: "file", Path: "/srv/strata/main/Docs/a.docx", User: "alice"}, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := s.verifyOffice(tok, "file"); err != nil || c.User != "alice" {
		t.Fatalf("valid: %v %+v", err, c)
	}
	if _, err := s.verifyOffice(tok, "cb"); err == nil {
		t.Error("file token accepted as a callback token")
	}
	old, _ := s.signOffice(officeClaims{Use: "file", Path: "/p", User: "alice"}, time.Hour, now.Add(-2*time.Hour))
	if _, err := s.verifyOffice(old, "file"); err == nil {
		t.Error("expired token accepted")
	}
	// Signed with the OnlyOffice secret instead of StrataOS's own key.
	forged, _ := jwt.Sign([]byte("shared-secret-0123456789"), officeClaims{Use: "file", Path: "/p", User: "admin", Exp: now.Add(time.Hour).Unix()})
	if _, err := s.verifyOffice(forged, "file"); err == nil {
		t.Error("token signed with another key accepted")
	}
	s.officeRoutes()
	for _, q := range []string{"", "?t=garbage", "?t=" + old} {
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/office/file"+q, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("file%s: %d", q, rec.Code)
		}
		rec = httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/office/callback"+q, strings.NewReader("{}")))
		if rec.Code != http.StatusForbidden {
			t.Errorf("callback%s: %d", q, rec.Code)
		}
	}
	if a, b := s.officeDocKey("/a", "v1"), s.officeDocKey("/a", "v1"); a != b || a == s.officeDocKey("/a", "v2") || len(a) > 128 {
		t.Error("document key must be stable per version and change with it")
	}
}

func TestCallbackAction(t *testing.T) {
	for status, want := range map[int]string{1: "ignore", 2: "save", 3: "error", 4: "ignore", 6: "save", 7: "error", 0: "unknown", 5: "unknown"} {
		if got := callbackAction(status); got != want {
			t.Errorf("status %d: %s, want %s", status, got, want)
		}
	}
}

func TestParseCallback(t *testing.T) {
	secret := []byte("shared-secret-0123456789")
	body := map[string]any{"key": "k1", "status": 2, "url": "http://192.168.1.5:8090/cache/files/x/output.docx?md5=a", "filetype": "docx"}
	tok, _ := jwt.Sign(secret, body)
	// In the body: the signed content wins over the plain fields.
	cb, err := parseCallback(secret, []byte(`{"key":"other","status":4,"token":"`+tok+`"}`), "")
	if err != nil || cb.Key != "k1" || cb.Status != 2 || cb.FileType != "docx" {
		t.Fatalf("body token: %v %+v", err, cb)
	}
	// In the header, wrapped in "payload".
	htok, _ := jwt.Sign(secret, map[string]any{"payload": body})
	if cb, err := parseCallback(secret, []byte(`{"key":"k1","status":2}`), "Bearer "+htok); err != nil || cb.Status != 2 {
		t.Fatalf("header token: %v %+v", err, cb)
	}
	if _, err := parseCallback(secret, []byte(`{"key":"k1","status":2}`), ""); err == nil {
		t.Error("unsigned callback accepted")
	}
	bad, _ := jwt.Sign([]byte("another-secret-xxxxxxxx"), body)
	if _, err := parseCallback(secret, []byte(`{"token":"`+bad+`"}`), ""); err == nil {
		t.Error("callback signed with another secret accepted")
	}
}

func TestOfficeFetchURL(t *testing.T) {
	cases := []struct{ internal, raw, want string }{
		{"http://127.0.0.1:8090", "http://192.168.1.5:8090/cache/files/k/output.docx?md5=x&expires=1", "http://127.0.0.1:8090/cache/files/k/output.docx?md5=x&expires=1"},
		// Another host in the callback: still fetched from the document server.
		{"http://127.0.0.1:8090", "http://169.254.169.254/latest/meta-data", "http://127.0.0.1:8090/latest/meta-data"},
		{"https://office.example.com/ds", "https://office.example.com/ds/cache/a.xlsx", "https://office.example.com/ds/ds/cache/a.xlsx"},
		{"http://127.0.0.1:8090", "file:///etc/shadow", ""},
		{"http://127.0.0.1:8090", "gopher://x/", ""},
		{"http://127.0.0.1:8090", "http://x/../../etc", ""},
		{"", "http://x/a", ""},
	}
	for _, c := range cases {
		got, err := officeFetchURL(c.internal, c.raw)
		if c.want == "" {
			if err == nil {
				t.Errorf("%s: accepted (%s)", c.raw, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.raw, got, err, c.want)
		}
	}
}

func TestCleanBaseURL(t *testing.T) {
	for raw, want := range map[string]string{"": "", "http://nas:8090/": "http://nas:8090", " https://o.example.com/ds ": "https://o.example.com/ds"} {
		if got, err := cleanBaseURL(raw); err != nil || got != want {
			t.Errorf("%q: %q %v", raw, got, err)
		}
	}
	for _, raw := range []string{"ftp://x", "http://", "http://u:p@x", "http://x/?a=1", "javascript:alert(1)", "http://x/\"><script>"} {
		if _, err := cleanBaseURL(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}
