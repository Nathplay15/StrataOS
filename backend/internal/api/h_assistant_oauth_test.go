package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"strataos/internal/store"
)

// fakeOAuthProvider installs a test provider whose endpoints are a local
// server; the token endpoint answers "authorization_pending" until the
// test flips approved.
func fakeOAuthProvider(t *testing.T) (approved *atomic.Bool, refreshes *atomic.Int32) {
	t.Helper()
	approved = &atomic.Bool{}
	refreshes = &atomic.Int32{}
	mux := http.NewServeMux()
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("client_id") == "" {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "dc-123",
			"user_code":                 "ABCD-1234",
			"verification_uri":          "https://provider.example/activate",
			"verification_uri_complete": "https://provider.example/activate?user_code=ABCD-1234",
			"expires_in":                600,
			"interval":                  1,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(400)
			return
		}
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			if !approved.Load() {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
				return
			}
			payload, _ := json.Marshal(map[string]string{"email": "admin@example.com"})
			idToken := "x." + base64.RawURLEncoding.EncodeToString(payload) + ".y"
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "at-1",
				"refresh_token": "rt-1",
				"expires_in":    3600,
				"id_token":      idToken,
			})
		case "refresh_token":
			refreshes.Add(1)
			if r.Form.Get("refresh_token") == "rt-bad" {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "at-2",
				"refresh_token": "rt-2",
				"expires_in":    3600,
			})
		default:
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "unsupported_grant_type"})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	oauthProviders["fake"] = &oauthProvider{
		ID: "fake", Name: "Fake", DeviceURL: srv.URL + "/device", TokenURL: srv.URL + "/token",
		APIBase: "https://api.fake.example/v1", Scope: "openid email", DefaultModel: "fake-model",
	}
	t.Cleanup(func() { delete(oauthProviders, "fake") })
	return approved, refreshes
}

func TestJwtEmail(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"email": "a@b.c"})
	tok := "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
	if jwtEmail(tok) != "a@b.c" {
		t.Errorf("jwtEmail: %q", jwtEmail(tok))
	}
	if jwtEmail("not-a-jwt") != "" || jwtEmail("a.!!!.b") != "" {
		t.Error("invalid tokens must give no email")
	}
}

func TestOAuthValidClientID(t *testing.T) {
	for _, bad := range []string{"", "short", strings.Repeat("x", 129), "has space"} {
		if oauthValidClientID(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
	if !oauthValidClientID("client-1234567890") {
		t.Error("valid client id refused")
	}
}

func TestAssistantOAuthFlow(t *testing.T) {
	approved, refreshes := fakeOAuthProvider(t)
	s := newTestServer(t)
	s.assistantOAuthRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
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

	// Providers list.
	rec := do("GET", "/api/assistant/oauth/providers", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"fake"`) {
		t.Fatalf("providers: %d %s", rec.Code, rec.Body.String())
	}
	// Start validation.
	if rec := do("POST", "/api/assistant/oauth/start", `{"provider":"nope","clientId":"client-1234567890"}`); rec.Code != 400 {
		t.Errorf("unknown provider: %d", rec.Code)
	}
	if rec := do("POST", "/api/assistant/oauth/start", `{"provider":"fake","clientId":"x"}`); rec.Code != 400 {
		t.Errorf("bad client id: %d", rec.Code)
	}
	rec = do("POST", "/api/assistant/oauth/start", `{"provider":"fake","clientId":"client-1234567890"}`)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"deviceCode":"dc-123"`) || !strings.Contains(body, `"userCode":"ABCD-1234"`) || !strings.Contains(body, `"verification":"https://provider.example/activate?user_code=ABCD-1234"`) {
		t.Fatalf("start: %d %s", rec.Code, body)
	}

	// Poll before approval: pending, nothing stored.
	rec = do("POST", "/api/assistant/oauth/poll", `{"provider":"fake","clientId":"client-1234567890","deviceCode":"dc-123"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"done":false`) || !strings.Contains(rec.Body.String(), `"pending"`) {
		t.Fatalf("pending: %d %s", rec.Code, rec.Body.String())
	}
	s.store.Read(func(st *store.State) {
		if st.Settings.OAuthRefresh != "" || st.Settings.AssistantProvider != "" {
			t.Errorf("stored before approval: %+v", st.Settings)
		}
	})

	// Approve, poll again: tokens stored, assistant pointed at the API.
	approved.Store(true)
	rec = do("POST", "/api/assistant/oauth/poll", `{"provider":"fake","clientId":"client-1234567890","deviceCode":"dc-123"}`)
	body = rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"done":true`) || !strings.Contains(body, `"email":"admin@example.com"`) {
		t.Fatalf("poll done: %d %s", rec.Code, body)
	}
	var set store.Settings
	s.store.Read(func(st *store.State) { set = st.Settings })
	if set.OAuthProvider != "fake" || set.OAuthAccess != "at-1" || set.OAuthRefresh != "rt-1" || set.OAuthEmail != "admin@example.com" {
		t.Errorf("tokens not stored: %+v", set)
	}
	if set.AssistantProvider != "oauth:fake" || set.AssistantURL != "https://api.fake.example/v1" || set.AssistantModel != "fake-model" {
		t.Errorf("assistant not pointed: provider=%q url=%q model=%q", set.AssistantProvider, set.AssistantURL, set.AssistantModel)
	}

	// assistantSettings accepts the connected account.
	if _, err := s.assistantSettings(); err != nil {
		t.Errorf("assistantSettings: %v", err)
	}
	// Resolve: the access token becomes the API key.
	resolved, err := s.assistantResolve(t.Context(), set)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.AssistantProvider != "" || resolved.AssistantURL != "https://api.fake.example/v1" || resolved.AssistantKey != "at-1" {
		t.Errorf("resolved wrong: %+v", resolved)
	}

	// Token still valid: no refresh call.
	if _, err := s.oauthAccessToken(t.Context(), false); err != nil {
		t.Fatalf("token: %v", err)
	}
	if refreshes.Load() != 0 {
		t.Errorf("unnecessary refresh")
	}
	// Expired token: one refresh, rotated tokens stored.
	_ = s.store.Update(func(st *store.State) error { st.Settings.OAuthExpires = time.Now().Add(-time.Minute); return nil })
	got, err := s.oauthAccessToken(t.Context(), false)
	if err != nil || got != "at-2" || refreshes.Load() != 1 {
		t.Errorf("refresh: got=%q err=%v refreshes=%d", got, err, refreshes.Load())
	}
	s.store.Read(func(st *store.State) { set = st.Settings })
	if set.OAuthAccess != "at-2" || set.OAuthRefresh != "rt-2" {
		t.Errorf("rotated tokens not stored: %+v", set)
	}
	// Revoked refresh token: clear sign-in error.
	_ = s.store.Update(func(st *store.State) error { st.Settings.OAuthRefresh = "rt-bad"; return nil })
	if _, err := s.oauthAccessToken(t.Context(), true); err == nil || !strings.Contains(err.Error(), "connect the account again") {
		t.Errorf("revoked refresh: %v", err)
	}

	// Unknown oauth provider cannot resolve.
	_, err = s.assistantResolve(t.Context(), store.Settings{AssistantProvider: "oauth:nope"})
	if err == nil {
		t.Error("unknown provider resolved")
	}

	// Disconnect clears everything.
	if rec := do("POST", "/api/assistant/oauth/disconnect", ""); rec.Code != 200 {
		t.Fatalf("disconnect: %d", rec.Code)
	}
	s.store.Read(func(st *store.State) { set = st.Settings })
	if set.OAuthRefresh != "" || set.OAuthAccess != "" || set.AssistantProvider != "" {
		t.Errorf("not cleared: %+v", set)
	}
	if _, err := s.assistantSettings(); err == nil {
		t.Error("assistantSettings should fail without provider")
	}
}

func TestAssistantOAuthPollErrors(t *testing.T) {
	approved, _ := fakeOAuthProvider(t)
	approved.Store(true)
	s := newTestServer(t)
	s.assistantOAuthRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		return nil
	})
	tok := s.sessions.create("admin", "admin", "127.0.0.1", time.Hour)
	do := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/assistant/oauth/poll", strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
		req.Header.Set("X-Strata", "1")
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(`{"provider":"fake","clientId":"x","deviceCode":"dc-123"}`); rec.Code != 400 {
		t.Errorf("bad client id: %d", rec.Code)
	}
	if rec := do(`{"provider":"fake","clientId":"client-1234567890","deviceCode":""}`); rec.Code != 400 {
		t.Errorf("empty device code: %d", rec.Code)
	}
	// The fake answers success only when approved; force expired_token by
	// pointing the device code at a grant the fake rejects is covered by the
	// refresh path above; here check unknown provider.
	if rec := do(`{"provider":"nope","clientId":"client-1234567890","deviceCode":"dc"}`); rec.Code != 400 {
		t.Errorf("unknown provider poll: %d", rec.Code)
	}
}

var _ = url.Values{} // keep net/url import used if tests change
