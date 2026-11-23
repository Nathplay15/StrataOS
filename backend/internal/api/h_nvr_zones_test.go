package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func TestNvrCameraZones(t *testing.T) {
	s := newTestServer(t)
	s.nvrRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		st.NVR.Folder = t.TempDir()
		return nil
	})
	tok := s.sessions.create("admin", "admin", "127.0.0.1", time.Hour)
	do := func(method, path string, body any) *httptest.ResponseRecorder {
		var rdr *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rdr)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
		req.Header.Set("X-Strata", "1")
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}

	// Invalid zones are rejected.
	rec := do("POST", "/api/nvr/cameras", map[string]any{
		"name": "Bad", "url": "rtsp://cam/live", "motion": true, "zones": "not json",
	})
	if rec.Code != 400 {
		t.Fatalf("invalid zones: %d %s", rec.Code, rec.Body.String())
	}
	rec = do("POST", "/api/nvr/cameras", map[string]any{
		"name": "Bad", "url": "rtsp://cam/live", "motion": true,
		"zones": `[{"x":0,"y":0,"w":0.2,"h":0.2},{"x":0,"y":0,"w":0.2,"h":0.2},{"x":0,"y":0,"w":0.2,"h":0.2},{"x":0,"y":0,"w":0.2,"h":0.2},{"x":0,"y":0,"w":0.2,"h":0.2}]`,
	})
	if rec.Code != 400 {
		t.Fatalf("too many zones: %d %s", rec.Code, rec.Body.String())
	}

	// Valid zones are stored normalized and returned by the list.
	rec = do("POST", "/api/nvr/cameras", map[string]any{
		"name": "Door", "url": "rtsp://cam/live", "motion": true,
		"zones": `[{"x":0.1,"y":0.2,"w":0.5,"h":0.6}]`,
	})
	if rec.Code != 200 {
		t.Fatalf("add with zones: %d %s", rec.Code, rec.Body.String())
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil || added.ID == "" {
		t.Fatalf("add response: %s", rec.Body.String())
	}
	rec = do("GET", "/api/nvr", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"zones":"[{\"x\":0.1,\"y\":0.2,\"w\":0.5,\"h\":0.6}]"`) {
		t.Fatalf("list without zones: %d %s", rec.Code, rec.Body.String())
	}

	// Clearing the zones works.
	rec = do("PUT", "/api/nvr/cameras/"+added.ID, map[string]any{
		"name": "Door", "url": "rtsp://cam/live", "motion": true, "zones": "",
	})
	if rec.Code != 200 {
		t.Fatalf("clear zones: %d %s", rec.Code, rec.Body.String())
	}
	rec = do("GET", "/api/nvr", nil)
	if strings.Contains(rec.Body.String(), `"zones"`) {
		t.Fatalf("zones not cleared: %s", rec.Body.String())
	}

	// Zones are stored again, then ignored (cleared) with the frigate engine.
	rec = do("PUT", "/api/nvr/cameras/"+added.ID, map[string]any{
		"name": "Door", "url": "rtsp://cam/live", "engine": "frigate",
		"zones": `[{"x":0,"y":0,"w":0.5,"h":0.5}]`,
	})
	if rec.Code != 200 {
		t.Fatalf("frigate with zones: %d %s", rec.Code, rec.Body.String())
	}
	rec = do("GET", "/api/nvr", nil)
	body := rec.Body.String()
	if strings.Contains(body, `"zones"`) || strings.Contains(body, `"motion":true`) {
		t.Fatalf("frigate engine must clear zones and motion: %s", body)
	}
}
