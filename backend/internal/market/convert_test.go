package market

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Portainer container template converts to a valid app: compose template,
// WEB_PORT variable, environment variables, and it passes Validate.
func TestPortainerContainerTemplate(t *testing.T) {
	b := []byte(`[
		{
			"type": 1,
			"name": "jellyfin",
			"title": "Jellyfin",
			"description": "Media server.",
			"categories": ["media"],
			"logo": "https://example.org/jf.svg",
			"image": "jellyfin/jellyfin:10.9.0",
			"ports": ["8096:8096/tcp", "1900:1900/udp"],
			"volumes": [{"/config": {"bind": "/srv/data/jellyfin", "mode": "rw"}}],
			"env": [
				{"name": "TZ", "label": "Time zone", "default": "Europe/Paris"},
				{"name": "token", "label": "API token"},
				{"name": "MODE", "preset": "standalone"}
			]
		}
	]`)
	cat, bad, err := ParseAny(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Apps) != 1 || len(bad) != 0 {
		t.Fatalf("apps %d, bad %v", len(cat.Apps), bad)
	}
	a := cat.Apps[0]
	if a.ID != "jellyfin" || a.Version != "10.9.0" || a.Category != "media" {
		t.Fatalf("app: %+v", a)
	}
	if a.WebPort != "WEB_PORT" {
		t.Fatalf("webPort: %q", a.WebPort)
	}
	if !strings.Contains(a.Compose, "${WEB_PORT}:8096") || !strings.Contains(a.Compose, "1900:1900/udp") {
		t.Fatalf("ports: %s", a.Compose)
	}
	if !strings.Contains(a.Compose, "/srv/data/jellyfin:/config") {
		t.Fatalf("volumes: %s", a.Compose)
	}
	if !strings.Contains(a.Compose, "TZ: ${TZ}") || !strings.Contains(a.Compose, "MODE: standalone") {
		t.Fatalf("env: %s", a.Compose)
	}
	keys := map[string]string{}
	for _, v := range a.Vars {
		keys[v.Key] = v.Type
	}
	if keys["WEB_PORT"] != "port" || keys["TZ"] != "text" || keys["TOKEN"] != "text" {
		t.Fatalf("vars: %+v", a.Vars)
	}
	if _, ok := keys["MODE"]; ok {
		t.Fatal("a preset value must not become a variable")
	}
}

// The wrapper form ({"templates": [...]}) and the "latest" fallback version.
func TestPortainerWrapperAndDefaults(t *testing.T) {
	b := []byte(`{"version": "2", "templates": [{"title": "Nginx", "image": "nginx", "type": 1}]}`)
	cat, _, err := ParseAny(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Apps) != 1 || cat.Apps[0].ID != "nginx" || cat.Apps[0].Version != "latest" {
		t.Fatalf("apps: %+v", cat.Apps)
	}
	// A broken template lands in the invalid list, the others stay available.
	b = []byte(`[{"title": "No image", "type": 1}, {"title": "Ok", "image": "nginx:1.0", "type": 1}]`)
	cat, bad, err := ParseAny(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Apps) != 1 || len(bad) != 1 || bad[0].ID != "no-image" {
		t.Fatalf("apps %d bad %+v", len(cat.Apps), bad)
	}
}

// A stack template fetches its compose file from its repository.
func TestPortainerStackTemplate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "services:\n  web:\n    image: nginx:1.27\n    ports:\n      - \"8080:80\"\n")
	}))
	defer srv.Close()
	b, _ := json.Marshal([]map[string]any{{
		"type": 2, "title": "My stack",
		"repository": map[string]any{"url": srv.URL, "stackfile": "docker-compose.yml"},
	}})
	cat, bad, err := ParseAny(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Apps) != 1 || len(bad) != 0 {
		t.Fatalf("apps %d, bad %v", len(cat.Apps), bad)
	}
	if !strings.Contains(cat.Apps[0].Compose, "nginx:1.27") {
		t.Fatalf("compose: %s", cat.Apps[0].Compose)
	}
}

// A CasaOS AppStore checkout (Apps/<name>/docker-compose.yml) converts: the
// data folder becomes DATA_DIR under /srv/strata, the first published port
// WEB_PORT, the refused top-level "name" goes, metadata come from x-casaos.
func TestCasaosCatalog(t *testing.T) {
	dir := t.TempDir()
	compose := `name: 2fauth
services:
  2fauth:
    image: 2fauth/2fauth:6.1.3
    network_mode: bridge
    ports:
      - target: 8000
        published: "8000"
        protocol: tcp
    restart: always
    volumes:
      - type: bind
        source: /DATA/AppData/$AppID
        target: /2fauth
    environment:
      APP_KEY: SomeRandomStringOf32CharsExactly
    container_name: 2fauth
x-casaos:
  id: org.icewhale.2fauth
  main: 2fauth
  author: ZimaOS Team
  category: Productivity
  title:
    en_US: 2FAuth
  description:
    en_US: Two-factor authentication web app.
`
	if err := os.MkdirAll(filepath.Join(dir, "Apps", "2FAuth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Apps", "2FAuth", "docker-compose.yml"), []byte(compose), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Apps", "2FAuth", "icon.svg"), []byte("<svg/>"), 0o640); err != nil {
		t.Fatal(err)
	}
	cat, err := casaosCatalog(dir, "CasaOS-AppStore", "https://raw.githubusercontent.com/me/store/HEAD/Apps")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Apps) != 1 {
		t.Fatalf("apps: %d", len(cat.Apps))
	}
	a := cat.Apps[0]
	if a.ID != "2fauth" || a.Name != "2FAuth" || a.Version != "6.1.3" || a.Category != "Productivity" {
		t.Fatalf("app: %+v", a)
	}
	if a.WebPort != "WEB_PORT" || a.Icon != "https://raw.githubusercontent.com/me/store/HEAD/Apps/2FAuth/icon.svg" {
		t.Fatalf("webPort %q icon %q", a.WebPort, a.Icon)
	}
	if hasTopLevelName(a.Compose) || strings.Contains(a.Compose, "/DATA/AppData") {
		t.Fatalf("compose not rewritten: %s", a.Compose)
	}
	if !strings.Contains(a.Compose, "source: ${DATA_DIR}") || !strings.Contains(a.Compose, "published: ${WEB_PORT}") {
		t.Fatalf("compose: %s", a.Compose)
	}
	// A converted app passes the strict validation (compose analysis included).
	if err := a.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

// hasTopLevelName tells whether the compose still has a top-level "name:" key.
func hasTopLevelName(compose string) bool {
	for _, line := range strings.Split(compose, "\n") {
		if strings.HasPrefix(line, "name:") {
			return true
		}
	}
	return false
}

// The strict StrataOS format still parses first, unchanged.
func TestParseAnyNativeFirst(t *testing.T) {
	b := []byte(`{"format": 1, "name": "Test", "apps": []}`)
	cat, _, err := ParseAny(b)
	if err != nil || cat.Name != "Test" {
		t.Fatalf("native: %v %+v", err, cat)
	}
	if _, _, err := ParseAny([]byte("not json at all")); err == nil {
		t.Fatal("garbage must fail")
	}
}
