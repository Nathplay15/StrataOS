package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"strataos/internal/docker"
	"strataos/internal/store"
)

// The config arrays must always be present in the JSON (even empty): the
// frontend reads config.guests / groups / ipsets / host.rules without a
// guard, and an omitted key (or a null) crashed the firewall page.
func TestFirewallConfigArraysAlwaysPresent(t *testing.T) {
	prevSock := docker.Socket
	docker.Socket = filepath.Join(t.TempDir(), "no-docker.sock") // no engine in tests
	t.Cleanup(func() { docker.Socket = prevSock })
	s := newTestServer(t)
	s.firewallRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		return nil
	})
	tok := s.sessions.create("admin", "admin", "127.0.0.1", time.Hour)
	req := httptest.NewRequest("GET", "/api/system/network/firewall", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
	req.Header.Set("X-Strata", "1")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("get firewall: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"guests":[]`, `"groups":[]`, `"ipsets":[]`, `"rules":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /api/system/network/firewall missing %s: %s", want, body)
		}
	}
}

func TestParseDomiflist(t *testing.T) {
	out := ` Interface   Type      Source    Model    MAC
-------------------------------------------------------------
 vnet3       bridge    br0       virtio   52:54:00:AA:bb:01
 -           network   default   virtio   52:54:00:aa:bb:02
`
	b := parseDomiflist(out)
	if !slices.Equal(b.MACs, []string{"52:54:00:aa:bb:01", "52:54:00:aa:bb:02"}) || !slices.Equal(b.Ifaces, []string{"vnet3"}) {
		t.Fatalf("%+v", b)
	}
}
