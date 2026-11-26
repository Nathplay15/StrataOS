package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func TestClientErrorsRateLimitedAndClipped(t *testing.T) {
	now := time.Now()
	for i := range clientErrorsMax {
		if !allowClientError("zed", now) {
			t.Fatalf("report %d refused", i)
		}
	}
	if allowClientError("zed", now) {
		t.Fatal("no limit")
	}
	if !allowClientError("zed", now.Add(11*time.Minute)) {
		t.Fatal("limit never ends")
	}
	if got := clip(strings.Repeat("é", 400), 501); len(got) > 504 || !strings.HasSuffix(got, "…") {
		t.Fatalf("clip: %d", len(got))
	}
}

func TestClientErrorLogged(t *testing.T) {
	s, tok := mobileTestServer(t)
	s.clientErrorRoutes()
	w := doReqBody(s, "POST", "/api/client-errors", tok, `{"page":"/files?path=/secret","message":"x is undefined","stack":"at f","kind":"render"}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got string
	s.store.Read(func(st *store.State) {
		if n := len(st.Events); n > 0 {
			got = st.Events[n-1].Message
		}
	})
	if got != "Interface error on /files: x is undefined" {
		t.Fatalf("logged %q", got)
	}
}

func doReqBody(s *Server, method, path, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("Authorization", "Bearer "+auth)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	return w
}
