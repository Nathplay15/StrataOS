package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestParseRef(t *testing.T) {
	for _, tc := range []struct {
		in                     string
		registry, repo, tag, d string
		name                   string
	}{
		{"nginx", "docker.io", "library/nginx", "latest", "", "nginx"},
		{"nginx:1.27", "docker.io", "library/nginx", "1.27", "", "nginx"},
		{"jellyfin/jellyfin:latest", "docker.io", "jellyfin/jellyfin", "latest", "", "jellyfin/jellyfin"},
		{"ghcr.io/home-assistant/home-assistant:stable", "ghcr.io", "home-assistant/home-assistant", "stable", "", "ghcr.io/home-assistant/home-assistant"},
		{"lscr.io/linuxserver/sonarr", "lscr.io", "linuxserver/sonarr", "latest", "", "lscr.io/linuxserver/sonarr"},
		{"localhost:5000/e2e-upd:v1", "localhost:5000", "e2e-upd", "v1", "", "localhost:5000/e2e-upd"},
		{"redis@sha256:" + strings.Repeat("a", 64), "docker.io", "library/redis", "", "sha256:" + strings.Repeat("a", 64), "redis"},
	} {
		r, err := ParseRef(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if r.Registry != tc.registry || r.Repo != tc.repo || r.Tag != tc.tag || r.Digest != tc.d || r.Name() != tc.name {
			t.Errorf("%s: got %+v (%s)", tc.in, r, r.Name())
		}
	}
	for _, bad := range []string{"", "Nginx", "a b", "nginx:", "nginx:-x", "redis@sha256:xyz", "-x/y"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseChallenge(t *testing.T) {
	c, ok := ParseChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/nginx:pull"`)
	if !ok || c.Realm != "https://auth.docker.io/token" || c.Service != "registry.docker.io" || c.Scope != "repository:library/nginx:pull" {
		t.Errorf("docker hub: %+v %v", c, ok)
	}
	c, ok = ParseChallenge(`Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:a/b:pull"`)
	if !ok || c.Service != "ghcr.io" {
		t.Errorf("ghcr: %+v", c)
	}
	if _, ok := ParseChallenge(`Basic realm="x"`); ok {
		t.Error("basic authentication accepted")
	}
	if _, ok := ParseChallenge(`Bearer service="x"`); ok {
		t.Error("challenge without realm accepted")
	}
}

func TestHasDigest(t *testing.T) {
	d := "sha256:" + strings.Repeat("b", 64)
	ref, _ := ParseRef("nginx:latest")
	if !HasDigest([]string{"nginx@" + d}, ref, d) || !HasDigest([]string{"docker.io/library/nginx@" + d}, ref, d) {
		t.Error("same digest not recognized")
	}
	if HasDigest([]string{"nginx@sha256:" + strings.Repeat("c", 64)}, ref, d) || HasDigest([]string{"other/nginx@" + d}, ref, d) || HasDigest(nil, ref, d) {
		t.Error("another digest or repository taken as current")
	}
}

// A registry asking for a token, like Docker Hub, served over plain HTTP on
// the loopback (allowed for local registries only).
func TestRemoteDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("d", 64)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:team/app:pull" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "tok"})
		case r.URL.Path == "/v2/team/app/manifests/v2":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="test"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if !strings.Contains(r.Header.Get("Accept"), "manifest.list.v2+json") {
				w.WriteHeader(http.StatusNotAcceptable)
				return
			}
			w.Header().Set("Docker-Content-Digest", digest)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	ref, err := ParseRef(host + "/team/app:v2")
	if err != nil {
		t.Fatal(err)
	}
	got, err := RemoteDigest(context.Background(), ref)
	if err != nil || got != digest {
		t.Errorf("digest: %q %v", got, err)
	}
	ref.Tag = "missing"
	if _, err := RemoteDigest(context.Background(), ref); err == nil {
		t.Error("missing tag gave a digest")
	}
	ref.Digest = digest
	if _, err := RemoteDigest(context.Background(), ref); err == nil {
		t.Error("pinned image checked")
	}
}

func TestLocalRegistry(t *testing.T) {
	for h, want := range map[string]bool{"localhost:5000": true, "127.0.0.1:5000": true, "[::1]:5000": true, "registry-1.docker.io": false, "10.0.0.2:5000": false, "localhost.evil.com": false} {
		if localRegistry(h) != want {
			t.Errorf("%s: want %v", h, want)
		}
	}
}

func TestRecreateSpec(t *testing.T) {
	var ctr, img map[string]any
	_ = json.Unmarshal([]byte(`{
		"Id": "0123456789abcdef0123",
		"Config": {"Hostname": "0123456789ab", "Image": "nginx:latest", "Cmd": ["nginx","-g","daemon off;"], "Entrypoint": ["/docker-entrypoint.sh"],
			"Env": ["PATH=/usr/bin", "NGINX_VERSION=1.27", "MY=1"], "Labels": {"maintainer": "NGINX", "app": "web"},
			"ExposedPorts": {"80/tcp": {}}, "WorkingDir": ""},
		"HostConfig": {"NetworkMode": "front", "Binds": ["/srv/strata/main/web:/usr/share/nginx/html:ro"], "RestartPolicy": {"Name": "unless-stopped"}},
		"Mounts": [{"Type": "bind", "Destination": "/usr/share/nginx/html"}, {"Type": "volume", "Name": "abc123", "Destination": "/var/cache/nginx", "RW": true}],
		"NetworkSettings": {"Networks": {
			"front": {"Aliases": ["web", "0123456789ab"], "IPAddress": "172.20.0.5", "NetworkID": "n1"},
			"back": {"IPAMConfig": {"IPv4Address": "172.21.0.9"}, "EndpointID": "e2"}}}
	}`), &ctr)
	_ = json.Unmarshal([]byte(`{"Config": {"Cmd": ["nginx","-g","daemon off;"], "Entrypoint": ["/docker-entrypoint.sh"], "Env": ["PATH=/usr/bin", "NGINX_VERSION=1.27"], "Labels": {"maintainer": "NGINX"}}}`), &img)
	body, extra := RecreateSpec(ctr, img)
	for _, k := range []string{"Cmd", "Entrypoint", "Hostname", "WorkingDir"} {
		if _, ok := body[k]; ok {
			t.Errorf("%s kept from the old image: %v", k, body[k])
		}
	}
	if env := body["Env"].([]any); len(env) != 1 || env[0] != "MY=1" {
		t.Errorf("env: %v", env)
	}
	if l := body["Labels"].(map[string]any); len(l) != 1 || l["app"] != "web" {
		t.Errorf("labels: %v", l)
	}
	if body["Image"] != "nginx:latest" || body["ExposedPorts"] == nil {
		t.Errorf("image or ports lost: %v", body)
	}
	binds := body["HostConfig"].(map[string]any)["Binds"].([]any)
	if !slices.Contains(binds, any("abc123:/var/cache/nginx")) || len(binds) != 2 {
		t.Errorf("anonymous volume not kept: %v", binds)
	}
	eps := body["NetworkingConfig"].(map[string]any)["EndpointsConfig"].(map[string]any)
	front := eps["front"].(map[string]any)
	if al := front["Aliases"].([]any); len(al) != 1 || al[0] != "web" || front["IPAddress"] != nil {
		t.Errorf("front endpoint: %v", front)
	}
	back, ok := extra["back"].(map[string]any)
	if !ok || back["IPAMConfig"] == nil || back["EndpointID"] != nil || len(extra) != 1 {
		t.Errorf("extra networks: %v", extra)
	}
	// The original inspect data is left untouched.
	if ctr["Config"].(map[string]any)["Cmd"] == nil {
		t.Error("RecreateSpec modified its input")
	}
	// A command of its own (not the image's) stays.
	ctr["Config"].(map[string]any)["Cmd"] = []any{"nginx", "-T"}
	if body, _ := RecreateSpec(ctr, img); body["Cmd"] == nil {
		t.Error("own command dropped")
	}
}
