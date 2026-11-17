package market

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strataos/internal/docker"
	"strataos/internal/netguard"
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

// A compose stack template (type 3) fetches its compose file when the
// catalog is converted, never when it is read; the result carries the
// compose file, so a change upstream changes what was approved.
func TestPortainerStackTemplate(t *testing.T) {
	compose := "services:\n  web:\n    image: nginx:1.27\n    ports:\n      - \"8080:80\"\n"
	var asked []string
	fetch := func(_ context.Context, u string) ([]byte, int, error) {
		asked = append(asked, u)
		if strings.HasPrefix(u, "https://raw.githubusercontent.com/me/tpl/HEAD/") {
			return []byte(compose), 200, nil
		}
		return nil, 404, errors.New("not found")
	}
	b, _ := json.Marshal([]map[string]any{{
		"type": 3, "title": "My stack",
		"repository": map[string]any{"url": "https://github.com/me/tpl", "stackfile": "stacks/web/docker-compose.yml"},
	}})
	// Read without converting: no network, the stack waits for a refresh.
	cat, bad, err := ParseAny(b)
	if err != nil || len(cat.Apps) != 0 || len(bad) != 1 || len(asked) != 0 {
		t.Fatalf("read: %v %+v %+v %v", err, cat, bad, asked)
	}
	conv, err := Convert(context.Background(), b, fetch)
	if err != nil {
		t.Fatal(err)
	}
	cat, bad, err = Parse(conv)
	if err != nil || len(cat.Apps) != 1 || len(bad) != 0 {
		t.Fatalf("converted: %v %+v %+v", err, cat, bad)
	}
	if !strings.Contains(cat.Apps[0].Compose, "nginx:1.27") {
		t.Fatalf("compose: %s", cat.Apps[0].Compose)
	}
	// Same source, compose changed upstream: the converted catalog (what the
	// administrator pinned) changes too.
	compose = strings.Replace(compose, "nginx:1.27", "nginx:1.28", 1)
	conv2, err := Convert(context.Background(), b, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if string(conv2) == string(conv) {
		t.Fatal("an upstream compose change must change the converted catalog")
	}
	// Only https repositories.
	b, _ = json.Marshal([]map[string]any{{"type": 3, "title": "Plain",
		"repository": map[string]any{"url": "http://example.com/tpl", "stackfile": "docker-compose.yml"}}})
	conv, _ = Convert(context.Background(), b, fetch)
	if _, bad, _ = Parse(conv); len(bad) != 1 || !strings.Contains(bad[0].Error, "https") {
		t.Fatalf("http repository: %+v", bad)
	}
}

// The real fetcher refuses internal addresses (and anything but https).
func TestFetchStackRefusesInternal(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "services: {}")
	}))
	defer srv.Close()
	if _, _, err := fetchStack(context.Background(), srv.URL+"/docker-compose.yml"); err == nil || !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("internal address: %v", err)
	}
	if _, _, err := fetchStack(context.Background(), "http://example.com/x.yml"); err == nil {
		t.Fatal("plain http accepted")
	}
}

// A real Portainer v2 list (portainer/templates, Lissy93 lists): volumes as
// {container, bind, readonly}, preset as a boolean, select lists, Swarm
// stacks (type 2) left out with a reason, compose stacks (type 3).
func TestPortainerRealList(t *testing.T) {
	b := []byte(`{
  "version": "2",
  "templates": [
    {
      "type": 1, "title": "Registry", "description": "Docker image registry",
      "categories": ["docker"], "platform": "linux",
      "logo": "https://portainer-io-assets.sfo2.digitaloceanspaces.com/logos/registry.png",
      "image": "registry:latest", "ports": ["5000/tcp"],
      "volumes": [{"container": "/var/lib/registry"}]
    },
    {
      "type": 1, "title": "MySQL", "description": "The most popular open-source database",
      "categories": ["database"], "platform": "linux", "image": "mysql:latest",
      "env": [{"name": "MYSQL_ROOT_PASSWORD", "label": "Root password"}],
      "ports": ["3306/tcp"], "volumes": [{"container": "/var/lib/mysql"}]
    },
    {
      "type": 1, "title": "Heimdall", "name": "heimdall", "image": "linuxserver/heimdall:2.6.1",
      "categories": ["Dashboard"], "restart_policy": "unless-stopped",
      "env": [
        {"name": "PUID", "label": "PUID", "default": "1000"},
        {"name": "PGID", "label": "PGID", "default": "1000"},
        {"name": "TZ", "label": "Timezone", "default": "Europe/London"},
        {"name": "MODE", "label": "Mode", "default": "prod", "preset": true},
        {"name": "THEME", "label": "Theme", "select": [{"text": "Dark", "value": "dark"}, {"text": "Light", "value": "light", "default": true}]}
      ],
      "ports": ["8080:80/tcp", "8443:443/tcp"],
      "volumes": [{"bind": "/srv/strata/apps/heimdall", "container": "/config"}, {"bind": "/srv/strata/media", "container": "/media", "readonly": true}]
    },
    {
      "type": 2, "title": "Portainer Agent", "description": "Manage all the resources in your Swarm cluster",
      "repository": {"url": "https://github.com/portainer/templates", "stackfile": "stacks/portainer-agent/docker-stack.yml"}
    },
    {
      "type": 3, "title": "Wordpress", "description": "Wordpress setup with a MySQL database",
      "categories": ["CMS"], "platform": "linux",
      "repository": {"url": "https://github.com/portainer/templates", "stackfile": "stacks/wordpress/docker-compose.yml"},
      "env": [{"name": "MYSQL_DATABASE_PASSWORD", "label": "Database root password", "description": "Password used by the MySQL root user."}]
    }
  ]
}`)
	wordpress := `version: "2"
services:
  db:
    image: mysql:5.7
    volumes:
      - db_data:/var/lib/mysql
    restart: always
    environment:
      MYSQL_ROOT_PASSWORD: ${MYSQL_DATABASE_PASSWORD}
      MYSQL_DATABASE: wordpress
      MYSQL_USER: wordpress
      MYSQL_PASSWORD: wordpress
  wordpress:
    image: wordpress:latest
    ports:
      - 80
    restart: always
    environment:
      WORDPRESS_DB_HOST: db:3306
      WORDPRESS_DB_USER: wordpress
      WORDPRESS_DB_PASSWORD: wordpress
volumes:
  db_data:
`
	fetch := func(_ context.Context, u string) ([]byte, int, error) {
		if u == "https://raw.githubusercontent.com/portainer/templates/HEAD/stacks/wordpress/docker-compose.yml" {
			return []byte(wordpress), 200, nil
		}
		return nil, 404, errors.New("not found")
	}
	conv, err := Convert(context.Background(), b, fetch)
	if err != nil {
		t.Fatal(err)
	}
	cat, bad, err := Parse(conv)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]App{}
	for _, a := range cat.Apps {
		ids[a.ID] = a
	}
	for _, id := range []string{"registry", "mysql", "heimdall", "wordpress"} {
		if _, ok := ids[id]; !ok {
			t.Fatalf("%s missing; apps %d, left out %+v", id, len(cat.Apps), bad)
		}
	}
	if len(bad) != 1 || bad[0].ID != "portainer-agent" || !strings.Contains(bad[0].Error, "Swarm") {
		t.Fatalf("left out: %+v", bad)
	}
	h := ids["heimdall"]
	for _, want := range []string{"${WEB_PORT}:80", "8443:443/tcp", "/srv/strata/apps/heimdall:/config", "/srv/strata/media:/media:ro", "MODE: prod", "THEME: ${THEME}", "TZ: ${TZ}"} {
		if !strings.Contains(h.Compose, want) {
			t.Fatalf("heimdall lacks %q:\n%s", want, h.Compose)
		}
	}
	vars := map[string]docker.AppVar{}
	for _, v := range h.Vars {
		vars[v.Key] = v
	}
	if vars["WEB_PORT"].Default != "8080" || vars["THEME"].Default != "light" || vars["TZ"].Default != "Europe/London" {
		t.Fatalf("heimdall vars: %+v", h.Vars)
	}
	if _, ok := vars["MODE"]; ok {
		t.Fatal("a preset variable must not be asked")
	}
	// Only the container port: proposed above 1024.
	if r := ids["registry"]; !strings.Contains(r.Compose, "${WEB_PORT}:5000") || !strings.Contains(r.Compose, "/var/lib/registry") {
		t.Fatalf("registry: %s", r.Compose)
	}
	m := ids["mysql"]
	if m.Vars[len(m.Vars)-1].Type != "password" {
		t.Fatalf("mysql vars: %+v", m.Vars)
	}
	if w := ids["wordpress"]; len(w.Vars) != 1 || w.Vars[0].Key != "MYSQL_DATABASE_PASSWORD" {
		t.Fatalf("wordpress vars: %+v", w.Vars)
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

// Real CasaOS AppStore apps use $PUID, $PGID and $TZ (filled in by CasaOS),
// long and "ip:host:container" ports, $AppID in several places, and
// locale maps with lower-case keys (review 0.12.0: most were refused).
func TestCasaosRealApps(t *testing.T) {
	dir := t.TempDir()
	apps := map[string]string{
		"Jellyfin": `name: jellyfin
services:
  jellyfin:
    image: linuxserver/jellyfin:10.10.7
    deploy:
      resources:
        reservations:
          memory: 256M
    network_mode: bridge
    ports:
      - target: 8096
        published: "8097"
        protocol: tcp
      - target: 7359
        published: "7359"
        protocol: udp
    restart: unless-stopped
    environment:
      PGID: $PGID
      PUID: $PUID
      TZ: $TZ
    volumes:
      - type: bind
        source: /DATA/AppData/$AppID/config
        target: /config
    devices:
      - /dev/dri:/dev/dri
    x-casaos:
      envs:
        - container: TZ
          description:
            en_us: TimeZone
      ports:
        - container: "8096"
          description:
            en_us: WebUI HTTP Port
    container_name: jellyfin
x-casaos:
  architectures:
    - amd64
    - arm64
  main: jellyfin
  author: CasaOS Team
  category: Media
  description:
    en_us: Jellyfin is the volunteer-built media solution.
  tagline:
    en_us: The Free Software Media System
  icon: https://cdn.jsdelivr.net/gh/IceWhaleTech/CasaOS-AppStore@main/Apps/Jellyfin/icon.png
  index: /
  port_map: "8097"
  title:
    en_us: Jellyfin
`,
		"Uptime-Kuma": `name: uptime-kuma
services:
  uptime-kuma:
    image: louislam/uptime-kuma:1.23.16
    container_name: uptime-kuma
    hostname: ${AppID}
    environment:
      TZ: ${TZ}
    ports:
      - 0.0.0.0:3001:3001/tcp
    restart: unless-stopped
    volumes:
      - /DATA/AppData/$AppID/data:/app/data
x-casaos:
  main: uptime-kuma
  author: CasaOS Team
  category: Utilities
  title:
    en_US: Uptime Kuma
`,
	}
	for folder, compose := range apps {
		if err := os.MkdirAll(filepath.Join(dir, "Apps", folder), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Apps", folder, "docker-compose.yml"), []byte(compose), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := casaosCatalog(dir, "CasaOS-AppStore", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(cat)
	parsed, bad, err := Parse(b)
	if err != nil || len(bad) != 0 || len(parsed.Apps) != 2 {
		t.Fatalf("parse: %v, left out %+v", err, bad)
	}
	for _, a := range parsed.Apps {
		vars := map[string]string{}
		for _, v := range a.Vars {
			vars[v.Key] = v.Default
		}
		switch a.ID {
		case "jellyfin":
			if a.Name != "Jellyfin" || a.Category != "Media" || vars["PUID"] != "1000" || vars["PGID"] != "1000" || vars["TZ"] == "" || vars["WEB_PORT"] != "8097" {
				t.Fatalf("jellyfin: %+v", a)
			}
			if !strings.Contains(a.Compose, "${DATA_DIR}/config") || strings.Contains(a.Compose, "AppID") || !strings.Contains(a.Compose, "published: \"7359\"") {
				t.Fatalf("jellyfin compose:\n%s", a.Compose)
			}
		case "uptime-kuma":
			if vars["WEB_PORT"] != "3001" || vars["TZ"] == "" {
				t.Fatalf("uptime-kuma vars: %+v", a.Vars)
			}
			if !strings.Contains(a.Compose, "0.0.0.0:${WEB_PORT}:3001/tcp") || !strings.Contains(a.Compose, "hostname: uptime-kuma") || !strings.Contains(a.Compose, "${DATA_DIR}/data:/app/data") {
				t.Fatalf("uptime-kuma compose:\n%s", a.Compose)
			}
		default:
			t.Fatalf("unexpected app %s", a.ID)
		}
	}
}

func TestParsePort(t *testing.T) {
	for in, want := range map[string]portSpec{
		"80":                  {Container: "80"},
		"8080:80":             {Host: "8080", Container: "80"},
		"8080:80/udp":         {Host: "8080", Container: "80", Proto: "udp"},
		"127.0.0.1:8080:80":   {IP: "127.0.0.1", Host: "8080", Container: "80"},
		"[::1]:8080:80/tcp":   {IP: "[::1]", Host: "8080", Container: "80", Proto: "tcp"},
		"0.0.0.0:3001:3001":   {IP: "0.0.0.0", Host: "3001", Container: "3001"},
		"9000-9001:9000-9001": {Host: "9000-9001", Container: "9000-9001"},
	} {
		if got := parsePort(in); got != want {
			t.Errorf("parsePort(%q) = %+v, want %+v", in, got, want)
		}
	}
}
