package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func TestMigrationRoutes(t *testing.T) {
	s := newTestServer(t)
	s.migrateRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		st.Shares = []*store.Share{{Name: "Main", Path: "/srv/strata/main"}}
		return nil
	})
	tok := s.sessions.create("admin", "admin", "127.0.0.1", time.Hour)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
		req.Header.Set("X-Strata", "1")
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}

	// Create.
	rec := do("POST", "/api/migrations", `{"name":"Old NAS","kind":"smb","host":"nas.local","share":"data","path":"photos","user":"bob","password":"secret","dest":"/Main/old","delete":true,"cron":"0 3 * * *"}`)
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "secret") || !strings.Contains(body, "********") {
		t.Errorf("the password must be masked: %s", body)
	}
	var created migrationView
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("no id in %s (%v)", body, err)
	}
	id := created.ID

	// Invalid ones are refused.
	for _, b := range []string{
		`{"name":"x","kind":"ftp","host":"h","share":"s","dest":"/Main/x"}`,
		`{"name":"x","kind":"smb","host":"h","share":"s","dest":"/Nope/x"}`,
		`{"name":"x","kind":"smb","host":"h","share":"s","dest":"/Main/../etc"}`,
		`{"name":"x","kind":"smb","host":"h","share":"s","dest":"/Main/x","cron":"bad"}`,
	} {
		if rec := do("POST", "/api/migrations", b); rec.Code != 400 {
			t.Errorf("POST %s: %d %s", b, rec.Code, rec.Body.String())
		}
	}

	// List, then update keeping the password.
	if rec := do("GET", "/api/migrations", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Old NAS") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	rec = do("PUT", "/api/migrations/"+id, `{"name":"Old NAS 2","kind":"rsync","host":"nas.local","share":"mod","user":"rsync","password":"","dest":"/Main/old2","enabled":true,"cron":"0 3 * * *"}`)
	if rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	var kept string
	s.store.Read(func(st *store.State) { kept = st.Migration(id).Password })
	if kept != "secret" {
		t.Errorf("an empty password must keep the saved one, got %q", kept)
	}
	// Another server: the saved password is not sent to it.
	if rec := do("PUT", "/api/migrations/"+id, `{"name":"Old NAS 2","kind":"rsync","host":"evil.example","share":"mod","user":"rsync","password":"********","dest":"/Main/old2","enabled":true,"cron":"0 3 * * *"}`); rec.Code != 400 {
		t.Errorf("masked password with a new host: %d %s", rec.Code, rec.Body.String())
	}

	// dueMigrations only fires enabled migrations with a matching cron.
	due := s.dueMigrations(time.Date(2026, 11, 23, 3, 0, 0, 0, time.Local)) // Monday 03:00
	if len(due) != 1 || due[0] != id {
		t.Errorf("due at 03:00 = %v", due)
	}
	if due := s.dueMigrations(time.Date(2026, 11, 23, 4, 0, 0, 0, time.Local)); len(due) != 0 {
		t.Errorf("due at 04:00 = %v", due)
	}

	// Delete.
	if rec := do("DELETE", "/api/migrations/"+id, ""); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("GET", "/api/migrations/"+id+"/run", ""); rec.Code != http.StatusNotFound && rec.Code != 404 {
		// run route on a missing migration must 404
		if rec := do("POST", "/api/migrations/"+id+"/run", ""); rec.Code != 404 {
			t.Errorf("run missing: %d %s", rec.Code, rec.Body.String())
		}
	}
}
