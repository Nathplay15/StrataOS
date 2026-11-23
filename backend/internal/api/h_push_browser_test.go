package api

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"strataos/internal/store"
)

func TestBrowserPush(t *testing.T) {
	s := newTestServer(t)
	s.browserPushRoutes()
	s.mobileRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "alice", Role: "admin"})
		return nil
	})
	alice := &jarClient{t: t, s: s, host: testHost, jar: map[string]string{cookieName: s.sessions.create("alice", "admin", "192.0.2.1", time.Hour)}}

	p256dh := make([]byte, 65)
	auth := make([]byte, 16)
	_, _ = rand.Read(p256dh)
	_, _ = rand.Read(auth)
	keys := map[string]string{
		"p256dh": base64.RawURLEncoding.EncodeToString(p256dh),
		"auth":   base64.RawURLEncoding.EncodeToString(auth),
	}
	endpoint := "https://push.example.org/sub/abc"

	// Not subscribed yet.
	code, st := alice.do("GET", "/api/me/push", nil)
	if code != 200 || st["enabled"] != false || st["vapid"] == "" {
		t.Fatalf("initial: %d %v", code, st)
	}

	// Bad endpoint / bad keys are refused.
	if code, _ := alice.do("POST", "/api/me/push/subscribe", map[string]any{"endpoint": "notaurl", "keys": keys}); code != http.StatusBadRequest {
		t.Fatalf("bad endpoint: %d", code)
	}
	if code, _ := alice.do("POST", "/api/me/push/subscribe", map[string]any{"endpoint": endpoint, "keys": map[string]string{"p256dh": "!!", "auth": keys["auth"]}}); code != http.StatusBadRequest {
		t.Fatalf("bad keys: %d", code)
	}

	// Subscribe.
	code, _ = alice.do("POST", "/api/me/push/subscribe", map[string]any{"endpoint": endpoint, "keys": keys, "name": "Firefox/132 Linux"})
	if code != 200 {
		t.Fatalf("subscribe: %d", code)
	}
	code, st = alice.do("GET", "/api/me/push", nil)
	if code != 200 || st["enabled"] != true || st["name"] != "Firefox/132 Linux" {
		t.Fatalf("after subscribe: %d %v", code, st)
	}

	// The browser appears as a push device, and is a push target (admin).
	code, devs := getList(t, alice, "/api/me/devices")
	if code != 200 || len(devs) != 1 {
		t.Fatalf("devices: %d %v", code, devs)
	}
	if devs[0]["kind"] != "browser" || devs[0]["push"] != true {
		t.Fatalf("device: %v", devs[0])
	}
	targets, n := s.mobilePushTargets()
	if n != 1 || len(targets) != 1 || !targets[0].dev.Browser {
		t.Fatalf("push targets: %d %v", n, targets)
	}

	// Re-subscribing the same endpoint updates, does not duplicate.
	if code, _ = alice.do("POST", "/api/me/push/subscribe", map[string]any{"endpoint": endpoint, "keys": keys}); code != 200 {
		t.Fatalf("re-subscribe: %d", code)
	}
	code, devs = getList(t, alice, "/api/me/devices")
	if code != 200 || len(devs) != 1 {
		t.Fatalf("devices after re-subscribe: %d %v", code, devs)
	}

	// Unsubscribe that endpoint.
	if code, _ := alice.do("DELETE", "/api/me/push", map[string]any{"endpoint": endpoint}); code != 200 {
		t.Fatalf("unsubscribe: %d", code)
	}
	code, st = alice.do("GET", "/api/me/push", nil)
	if code != 200 || st["enabled"] != false {
		t.Fatalf("after unsubscribe: %d %v", code, st)
	}
}
