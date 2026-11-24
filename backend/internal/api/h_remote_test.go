package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"strataos/internal/docker"
)

// The apps list of GET /api/remote/domains must give addresses usable from
// outside the NAS: the host the client used, not 127.0.0.1 (which only works
// on the NAS itself — the mobile launcher would open localhost on the phone).
func TestAppExternalUsesRequestHost(t *testing.T) {
	dir := t.TempDir()
	old := docker.StacksDir
	docker.StacksDir = dir
	t.Cleanup(func() { docker.StacksDir = old })
	if err := os.MkdirAll(filepath.Join(dir, "myapp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "myapp", ".strata-custom.json"), []byte(`{"name":"MyApp","webPort":8096}`), 0o600); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "http://x/api/remote/domains", nil)
	r.Host = "nas.local:7070"
	u, err := appExternal(r, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != "http://nas.local:8096" {
		t.Errorf("host name: got %s", u.String())
	}

	r.Host = "192.168.1.50:7070"
	u, err = appExternal(r, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != "http://192.168.1.50:8096" {
		t.Errorf("ip: got %s", u.String())
	}

	// The proxy target itself must stay on 127.0.0.1 (the NAS proxies locally).
	u, err = appTarget("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != "http://127.0.0.1:8096" {
		t.Errorf("proxy target changed: %s", u.String())
	}
}
