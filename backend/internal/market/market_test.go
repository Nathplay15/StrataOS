package market

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"strataos/internal/docker"
)

const stackDir = "/var/lib/strata/stacks/demo"

func analyze(t *testing.T, compose string, env map[string]string) (*Grants, error) {
	t.Helper()
	return Analyze(compose, env, stackDir, true)
}

func TestInterpolate(t *testing.T) {
	env := map[string]string{"A": "x", "E": ""}
	for in, want := range map[string]string{
		"plain": "plain", "${A}": "x", "$A/b": "x/b", "${E:-def}": "def", "${E-def}": "", "${A:+on}": "on", "${E:+on}": "",
		"$$A": "$A", "a$${A}": "a${A}",
	} {
		got, err := Interpolate(in, env)
		if err != nil || got != want {
			t.Errorf("Interpolate(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"${HOME}", "$HOME", "${HOME:-/root}", "${A:-${B}}", "${A", "a$", "$-", "${E:?required}"} {
		if _, err := Interpolate(bad, env); err == nil {
			t.Errorf("Interpolate(%q) should fail", bad)
		}
	}
}

func TestAnalyzeGrants(t *testing.T) {
	compose := `services:
  web:
    image: nginx:1.27
    restart: unless-stopped
    ports:
      - "${WEB_PORT}:80"
      - 127.0.0.1:5353:53/udp
      - target: 443
        published: "8443"
    volumes:
      - ${DATA_DIR}:/data
      - ./conf:/etc/nginx/conf.d:ro
      - cache:/cache
      - /etc/localtime:/etc/localtime:ro
    environment:
      - PASSWORD=${PASS}
  helper:
    image: alpine:3
    network_mode: host
    privileged: true
    pid: host
    cap_add: [NET_ADMIN]
    security_opt: ["apparmor:unconfined"]
    devices: ["/dev/ttyUSB0:/dev/ttyUSB0"]
    volumes:
      - type: bind
        source: /var/run/docker.sock
        target: /var/run/docker.sock
volumes:
  cache:
`
	g, err := analyze(t, compose, map[string]string{"WEB_PORT": "8080", "DATA_DIR": "/srv/strata/main/apps/demo", "PASS": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Ports) != 3 || g.Ports[0].Published != "8080" || g.Ports[1].HostIP != "127.0.0.1" || g.Ports[1].Protocol != "udp" || g.Ports[2].Published != "8443" {
		t.Errorf("ports: %+v", g.Ports)
	}
	if len(g.Folders) != 2 || g.Folders[0].Source != "/srv/strata/main/apps/demo" || g.Folders[1].Source != stackDir+"/conf" || !g.Folders[1].ReadOnly {
		t.Errorf("folders: %+v", g.Folders)
	}
	hp := []string{}
	for _, m := range g.HostPaths {
		hp = append(hp, m.Source)
	}
	slices.Sort(hp)
	if strings.Join(hp, ",") != "/etc/localtime,/var/run/docker.sock" || !g.DockerSocket {
		t.Errorf("host paths: %v, docker socket %v", hp, g.DockerSocket)
	}
	if strings.Join(g.Privileged, ",") != "helper" || strings.Join(g.HostNetwork, ",") != "helper" || strings.Join(g.HostNamespaces, ",") != "helper: pid" ||
		strings.Join(g.Devices, ",") != "/dev/ttyUSB0" || strings.Join(g.Capabilities, ",") != "NET_ADMIN" || strings.Join(g.Volumes, ",") != "cache" ||
		strings.Join(g.Images, ",") != "alpine:3,nginx:1.27" {
		t.Errorf("grants: %+v", g)
	}

	req := g.Required()
	if err := (Approval{}).Covers(req); err == nil {
		t.Error("an empty approval covers risky grants")
	}
	partial := req
	partial.Privileged = false
	if err := partial.Covers(req); err == nil || !strings.Contains(err.Error(), "privileged") {
		t.Errorf("missing privileged approval: %v", err)
	}
	partial = req
	partial.HostPaths = []string{"/etc/localtime"}
	if err := partial.Covers(req); err == nil || !strings.Contains(err.Error(), "docker.sock") {
		t.Errorf("missing host path approval: %v", err)
	}
	if err := req.Covers(req); err != nil {
		t.Error(err)
	}
}

func TestAnalyzeSafeApp(t *testing.T) {
	g, err := analyze(t, "services:\n  w:\n    image: traefik/whoami\n    ports: [\"${P}:80\"]\n", map[string]string{"P": "8099"})
	if err != nil {
		t.Fatal(err)
	}
	if !g.Required().Empty() {
		t.Errorf("a plain app needs approvals: %+v", g.Required())
	}
}

func TestAnalyzeRefusals(t *testing.T) {
	env := map[string]string{"D": "/srv/strata/x"}
	for name, compose := range map[string]string{
		"build":              "services:\n  a:\n    build: .\n",
		"env_file":           "services:\n  a:\n    image: x\n    env_file: /etc/shadow\n",
		"extends":            "services:\n  a:\n    image: x\n    extends: {file: /etc/x.yml, service: b}\n",
		"include":            "include: [/etc/compose.yml]\nservices:\n  a:\n    image: x\n",
		"secrets":            "services:\n  a:\n    image: x\n    secrets: [s]\nsecrets:\n  s: {file: /etc/shadow}\n",
		"volumes_from":       "services:\n  a:\n    image: x\n    volumes_from: [other]\n",
		"no image":           "services:\n  a:\n    command: sh\n",
		"undeclared var":     "services:\n  a:\n    image: x\n    volumes: [\"${HOME}:/h\"]\n",
		"escape stack":       "services:\n  a:\n    image: x\n    volumes: [\"../../../../etc:/e\"]\n",
		"home":               "services:\n  a:\n    image: x\n    volumes: [\"~/.ssh:/s\"]\n",
		"external volume":    "services:\n  a:\n    image: x\n    volumes: [\"v:/v\"]\nvolumes:\n  v: {external: true}\n",
		"named other volume": "services:\n  a:\n    image: x\n    volumes: [\"v:/v\"]\nvolumes:\n  v: {name: portainer_data}\n",
		"bind in disguise":   "services:\n  a:\n    image: x\n    volumes: [\"v:/v\"]\nvolumes:\n  v: {driver_opts: {type: none, o: bind, device: /etc}}\n",
		"undeclared volume":  "services:\n  a:\n    image: x\n    volumes: [\"v:/v\"]\n",
		"macvlan":            "services:\n  a:\n    image: x\nnetworks:\n  n: {driver: macvlan}\n",
		"external network":   "services:\n  a:\n    image: x\nnetworks:\n  n: {external: true}\n",
		"container netns":    "services:\n  a:\n    image: x\n    network_mode: container:other\n",
		"pid container":      "services:\n  a:\n    image: x\n    pid: container:other\n",
		"bad device":         "services:\n  a:\n    image: x\n    devices: [\"/etc/shadow:/dev/x\"]\n",
		"rshared":            "services:\n  a:\n    image: x\n    volumes: [\"${D}:/d:rshared\"]\n",
		"runtime":            "services:\n  a:\n    image: x\n    runtime: nvidia\n",
		"cgroup_parent":      "services:\n  a:\n    image: x\n    cgroup_parent: /\n",
		"var in key":         "services:\n  a:\n    image: x\n    ${D}: 1\n",
		"syslog logging":     "services:\n  a:\n    image: x\n    logging: {driver: syslog, options: {syslog-address: \"udp://1.2.3.4\"}}\n",
		"project name":       "name: other\nservices:\n  a:\n    image: x\n",
	} {
		if _, err := analyze(t, compose, env); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := analyze(t, "services:\n  a:\n    image: x\n", map[string]string{"COMPOSE_FILE": "/etc/x"}); err == nil {
		t.Error("COMPOSE_FILE variable accepted")
	}
}

// TestAnalyzeComposeOutput reads the normalized output of docker compose
// config, as the second check does (with docker compose when it is here).
func TestAnalyzeComposeOutput(t *testing.T) {
	out := `{"name":"demo","networks":{"default":{"name":"demo_default","ipam":{}}},"services":{"web":{"command":null,"entrypoint":null,"image":"nginx",
"privileged":true,"network_mode":"host","devices":[{"source":"/dev/dri","target":"/dev/dri","permissions":"rwm"}],
"ports":[{"mode":"ingress","host_ip":"127.0.0.1","target":80,"published":"8080","protocol":"tcp"}],
"volumes":[{"type":"bind","source":"/srv/strata/a","target":"/a","read_only":true,"bind":{}},{"type":"bind","source":"/etc","target":"/e","bind":{}},
{"type":"volume","source":"data","target":"/d","volume":{}}]}},"volumes":{"data":{"name":"demo_data"}}}`
	g, err := Analyze(out, nil, stackDir, false)
	if err != nil {
		t.Fatal(err)
	}
	r := g.Required()
	if !r.Privileged || !r.HostNetwork || strings.Join(r.HostPaths, ",") != "/etc" || strings.Join(r.Devices, ",") != "/dev/dri" || len(g.Folders) != 1 || g.Ports[0].Published != "8080" {
		t.Errorf("grants of the compose output: %+v", g)
	}

	if _, err := exec.LookPath("docker"); err != nil || exec.Command("docker", "compose", "version").Run() != nil {
		t.Skip("docker compose not installed")
	}
	dir := t.TempDir()
	tmpl := "services:\n  a:\n    image: alpine\n    volumes:\n      - ${D}:/d\n      - ./x:/x\n      - /etc/hostname:/h:ro\n    ports: [\"${P}:80/udp\"]\n    cap_add: [SYS_ADMIN]\n"
	env := map[string]string{"D": "/srv/strata/main/d", "P": "9999"}
	g1, err := Analyze(tmpl, env, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(tmpl), 0o644)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("D=\"/srv/strata/main/d\"\nP=\"9999\"\n"), 0o644)
	b, err := exec.Command("docker", "compose", "-p", "demo", "--project-directory", dir, "-f", filepath.Join(dir, "compose.yaml"), "config", "--format", "json").Output()
	if err != nil {
		t.Fatal(err)
	}
	g2, err := Analyze(string(b), nil, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	j1, _ := json.Marshal(g1.Required())
	j2, _ := json.Marshal(g2.Required())
	if string(j1) != string(j2) || len(g1.Folders) != len(g2.Folders) || g2.Ports[0].Published != "9999" || g2.Ports[0].Protocol != "udp" {
		t.Errorf("the two checks differ:\n%s %+v\n%s %+v", j1, g1, j2, g2)
	}
}

func sampleCatalog() Catalog {
	return Catalog{Format: 1, Name: "Test catalog", Maintainer: "Tests", Apps: []App{
		{ID: "whoami", Name: "Who am I", Version: "1.0.0", Maintainer: "Tests", Category: "Tools", WebPort: "WEB_PORT",
			Vars:    []docker.AppVar{{Key: "WEB_PORT", Label: "Web port", Default: "8099", Type: "port"}},
			Compose: "services:\n  whoami:\n    image: traefik/whoami:v1.10\n    ports: [\"${WEB_PORT}:80\"]\n"},
		{ID: "bad", Name: "Bad", Version: "1", Maintainer: "x", Compose: "services:\n  a:\n    build: .\n"},
		{ID: "BAD id", Name: "Bad", Version: "1", Maintainer: "x", Compose: "services:\n  a:\n    image: x\n"},
		{ID: "pw", Name: "Password default", Version: "1", Maintainer: "x", Vars: []docker.AppVar{{Key: "P", Label: "P", Type: "password", Default: "12345678"}}, Compose: "services:\n  a:\n    image: x\n"},
		{ID: "from", Name: "Link", Version: "1", Maintainer: "x", Vars: []docker.AppVar{{Key: "P", Label: "P", Type: "text", From: "{x.Y}"}}, Compose: "services:\n  a:\n    image: x\n"},
		{ID: "whoami", Name: "Duplicate", Version: "1", Maintainer: "x", Compose: "services:\n  a:\n    image: x\n"},
	}}
}

func TestParse(t *testing.T) {
	b, _ := json.Marshal(sampleCatalog())
	c, invalid, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Apps) != 1 || c.Apps[0].ID != "whoami" || len(invalid) != 5 {
		t.Errorf("apps %d, invalid %+v", len(c.Apps), invalid)
	}
	for name, bad := range map[string]string{
		"format":  `{"format":2,"name":"x","apps":[]}`,
		"unknown": `{"format":1,"name":"x","apps":[],"script":"x"}`,
		"name":    `{"format":1,"name":"","apps":[]}`,
		"json":    `not json`,
	} {
		if _, _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestExampleCatalog(t *testing.T) {
	b, err := os.ReadFile("../../../packaging/catalog-example/catalog.json")
	if err != nil {
		t.Skip("no example catalog")
	}
	c, invalid, err := Parse(b)
	if err != nil || len(invalid) > 0 || len(c.Apps) == 0 {
		t.Fatalf("example catalog: %v %+v", err, invalid)
	}
	sig, err := os.ReadFile("../../../packaging/catalog-example/catalog.json.sig")
	if err != nil {
		t.Fatal("example catalog not signed")
	}
	pub, err := os.ReadFile("../../../packaging/catalog-example/public-key.txt")
	if err != nil {
		t.Fatal(err)
	}
	k, err := ParsePublicKey(string(pub))
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(k, b, sig); err != nil {
		t.Errorf("example catalog signature: %v (re-sign it after a change, see its README)", err)
	}
}

func TestParseLocation(t *testing.T) {
	for in, want := range map[string]Location{
		"https://example.org/catalog.json":           {Kind: KindHTTPS, URL: "https://example.org/catalog.json"},
		"https://github.com/me/apps.git":             {Kind: KindGit, URL: "https://github.com/me/apps.git"},
		"git+https://codeberg.org/me/apps#stable":    {Kind: KindGit, URL: "https://codeberg.org/me/apps", Ref: "stable"},
		"http://127.0.0.1:8879/catalog.json":         {Kind: KindHTTPS, URL: "http://127.0.0.1:8879/catalog.json"},
		" https://example.org/c.json?token=abc ":     {Kind: KindHTTPS, URL: "https://example.org/c.json?token=abc"},
		"https://github.com/me/apps.git#v1.2-branch": {Kind: KindGit, URL: "https://github.com/me/apps.git", Ref: "v1.2-branch"},
	} {
		got, err := ParseLocation(in)
		if err != nil || got != want {
			t.Errorf("ParseLocation(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"http://example.org/c.json", "ftp://x/c.json", "file:///etc/passwd", "git+file:///tmp/r", "https://u:p@x/c.json",
		"git+ssh://git@x/r", "https://x/r.git#--upload-pack=sh", "https://x/c.json#b", "javascript:alert(1)", "https://x/a b"} {
		if _, err := ParseLocation(bad); err == nil {
			t.Errorf("ParseLocation(%q) accepted", bad)
		}
	}
}

func TestSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	index := []byte(`{"format":1}`)
	sig := ed25519.Sign(priv, index)
	k, err := ParsePublicKey(FormatPublicKey(pub))
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(k, index, sig); err != nil {
		t.Error(err)
	}
	if err := Verify(k, index, []byte(base64.StdEncoding.EncodeToString(sig)+"\n")); err != nil {
		t.Error("base64 signature:", err)
	}
	if Verify(k, []byte(`{"format":2}`), sig) == nil {
		t.Error("tampered index accepted")
	}
	if Verify(k, index, nil) == nil {
		t.Error("missing signature accepted")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if Verify(other, index, sig) == nil {
		t.Error("signature of another key accepted")
	}
	if _, err := ParsePublicKey("ed25519:AAAA"); err == nil {
		t.Error("short key accepted")
	}
	pemKey := "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEAGb9ECWmEzf6FQbrBZ9w7lshQhqowtrbLDFw4rXAxZuE=\n-----END PUBLIC KEY-----\n"
	if _, err := ParsePublicKey(pemKey); err != nil {
		t.Error("PEM key:", err)
	}
}

func TestFetchGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	allowFileURLs = true
	defer func() { allowFileURLs = false }()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.MkdirAll(repo, 0o755)
	run("init", "-q", "-b", "main")
	b, _ := json.Marshal(sampleCatalog())
	os.WriteFile(filepath.Join(repo, "catalog.json"), b, 0o644)
	os.Symlink("/etc/passwd", filepath.Join(repo, "catalog.json.sig"))
	run("add", "-A")
	run("commit", "-q", "-m", "c")
	loc, err := ParseLocation("git+file://" + repo + "#main")
	if err != nil {
		t.Fatal(err)
	}
	f, err := Fetch(context.Background(), loc)
	if err != nil {
		t.Fatal(err)
	}
	if string(f.Index) != string(b) || len(f.Commit) != 40 {
		t.Errorf("fetched %d bytes, commit %q", len(f.Index), f.Commit)
	}
	// The symlink is checked out as a plain file holding its target: never followed.
	if strings.Contains(string(f.Sig), "root:") {
		t.Error("a symlink of the repository was followed")
	}
}
