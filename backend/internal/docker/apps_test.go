package docker

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Ports used by StrataOS itself: interface, app domains, DLNA, iSCSI, OwnTone.
var reservedPorts = map[string]bool{"7070": true, "7443": true, "80": true, "443": true, "8200": true, "3260": true, "3689": true}

func TestCatalog(t *testing.T) {
	ids, ports := map[string]bool{}, map[string]string{}
	ref := regexp.MustCompile(`\$\{([A-Z_]+)(:-[^}]*)?\}`)
	for _, a := range Catalog {
		if ids[a.ID] {
			t.Errorf("duplicate app id %s", a.ID)
		}
		ids[a.ID] = true
		if a.Name == "" || a.Category == "" || a.Description == "" || !strings.HasPrefix(a.Icon, "https://") {
			t.Errorf("%s: name, category, description and icon are required", a.ID)
		}
		vars := map[string]bool{}
		for _, v := range a.Vars {
			vars[v.Key] = true
			if v.Type == "port" {
				if other, ok := ports[v.Default]; ok {
					t.Errorf("%s and %s both default to port %s", a.ID, other, v.Default)
				}
				ports[v.Default] = a.ID
				// Nginx Proxy Manager is a reverse proxy: 80/443 are its job.
				if reservedPorts[v.Default] && a.ID != "npm" {
					t.Errorf("%s: port %s is used by StrataOS", a.ID, v.Default)
				}
			}
			if v.Type == "path" && v.Default != "/srv/strata" && !strings.HasPrefix(v.Default, "/srv/strata/") {
				t.Errorf("%s: %s is outside /srv/strata", a.ID, v.Key)
			}
		}
		if a.WebPort != "" && !vars[a.WebPort] {
			t.Errorf("%s: web port %s is not a variable", a.ID, a.WebPort)
		}
		for _, m := range ref.FindAllStringSubmatch(a.Compose, -1) {
			if !vars[m[1]] && m[2] == "" {
				t.Errorf("%s: ${%s} has no value", a.ID, m[1])
			}
		}
	}
	// Linked defaults point to existing apps and variables.
	for _, a := range Catalog {
		for _, v := range a.Vars {
			for _, m := range linkRe.FindAllStringSubmatch(v.From, -1) {
				o := FindApp(m[1])
				found := false
				for _, ov := range o.varsOrNil() {
					found = found || ov.Key == m[2]
				}
				if !found {
					t.Errorf("%s: %s links to unknown %s.%s", a.ID, v.Key, m[1], m[2])
				}
			}
		}
	}
}

func (a *App) varsOrNil() []AppVar {
	if a == nil {
		return nil
	}
	return a.Vars
}

// defaults returns install values as the App Center would send them.
func (a *App) defaults() map[string]string {
	vals := map[string]string{}
	for _, v := range a.Vars {
		if v.Type == "password" {
			n := max(v.Length, 20)
			vals[v.Key] = strings.Repeat("s", n)
		}
	}
	return vals
}

// interpolate substitutes ${KEY} and ${KEY:-default} like docker compose.
func interpolate(compose string, env map[string]string) string {
	ref := regexp.MustCompile(`\$\$|\$\{([A-Z_]+)(:-([^}]*))?\}`)
	return ref.ReplaceAllStringFunc(compose, func(m string) string {
		if m == "$$" {
			return "$"
		}
		p := ref.FindStringSubmatch(m)
		if v, ok := env[p[1]]; ok && v != "" {
			return v
		}
		return p[3]
	})
}

func parseEnv(env string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(env, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			out[k] = strings.Trim(v, `"`)
		}
	}
	return out
}

// Every catalog entry renders a usable compose file with its default values.
func TestCatalogRendersDefaults(t *testing.T) {
	composeOK := exec.Command("docker", "compose", "version").Run() == nil
	service := regexp.MustCompile(`(?m)^  ([a-z0-9_-]+):\n((?:    .*\n|\n)*)`)
	for _, a := range Catalog {
		env, _, err := a.RenderEnv(a.defaults())
		if err != nil {
			t.Errorf("%s: %v", a.ID, err)
			continue
		}
		yml := interpolate(a.Compose, parseEnv(env))
		if strings.Contains(yml, "\t") || strings.Contains(yml, "${") || !strings.HasPrefix(yml, "services:\n") {
			t.Errorf("%s: bad compose file:\n%s", a.ID, yml)
		}
		svcs := service.FindAllStringSubmatch(strings.SplitN(yml, "\nvolumes:\n", 2)[0], -1)
		if len(svcs) == 0 {
			t.Errorf("%s: no service", a.ID)
		}
		for _, s := range svcs {
			if !strings.Contains(s[2], "    image: ") || strings.Count(s[2], "    image: ") != 1 {
				t.Errorf("%s/%s: no image", a.ID, s[1])
			}
			if !strings.Contains(s[2], "    restart: unless-stopped\n") {
				t.Errorf("%s/%s: no restart policy", a.ID, s[1])
			}
		}
		if !composeOK {
			continue
		}
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(a.Compose), 0o600)
		_ = os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600)
		if out, err := exec.Command("docker", "compose", "--project-directory", dir, "-p", a.ID, "config", "-q").CombinedOutput(); err != nil {
			t.Errorf("%s: docker compose config: %v %s", a.ID, err, out)
		}
	}
}

func TestRenderEnvTypes(t *testing.T) {
	a := &App{Vars: []AppVar{{Key: "DEV", Label: "Device", Type: "device"}, {Key: "KEY", Label: "Key", Type: "password", Length: 32}}}
	for _, tc := range []struct {
		dev, key string
		ok       bool
	}{
		{"/dev/serial/by-id/usb-ITead_Sonoff_Zigbee_3.0-if00-port0", strings.Repeat("k", 32), true},
		{"/dev/ttyACM0", strings.Repeat("k", 32), true},
		{"/srv/strata/x", strings.Repeat("k", 32), false},
		{"/dev/../etc/shadow", strings.Repeat("k", 32), false},
		{"/dev/tty USB0", strings.Repeat("k", 32), false},
		{"/dev/ttyUSB0", strings.Repeat("k", 20), false},
	} {
		_, dirs, err := a.RenderEnv(map[string]string{"DEV": tc.dev, "KEY": tc.key})
		if (err == nil) != tc.ok || len(dirs) != 0 {
			t.Errorf("%s/%d: err=%v dirs=%v", tc.dev, len(tc.key), err, dirs)
		}
	}
}

func TestLinked(t *testing.T) {
	z := FindApp("zigbee2mqtt")
	none := z.Linked(func(string, string) (string, bool) { return "", false })
	if len(none) != 0 {
		t.Errorf("linked without mosquitto: %v", none)
	}
	env := map[string]string{"MQTT_PORT": "1884", "MQTT_USER": "home", "MQTT_PASSWORD": "secret-pass"}
	got := z.Linked(func(app, key string) (string, bool) { return env[key], app == "mosquitto" })
	if got["MQTT_SERVER"] != "mqtt://host.docker.internal:1884" || got["MQTT_USER"] != "home" || got["MQTT_PASSWORD"] != "secret-pass" {
		t.Errorf("got %v", got)
	}
	w := FindApp("openwebui").Linked(func(app, key string) (string, bool) { return "11500", app == "ollama" && key == "API_PORT" })
	if w["OLLAMA_URL"] != "http://host.docker.internal:11500" {
		t.Errorf("open webui: %v", w)
	}
}

func TestCustomAppValidate(t *testing.T) {
	for _, tc := range []struct {
		c  CustomApp
		ok bool
	}{
		{CustomApp{Name: "Whoami", WebPort: 8099}, true},
		{CustomApp{Name: "  My app ", Icon: "https://example.com/icon.svg", Description: "Test"}, true},
		{CustomApp{Name: ""}, false},
		{CustomApp{Name: strings.Repeat("a", 65)}, false},
		{CustomApp{Name: "a\nb"}, false},
		{CustomApp{Name: "x", Icon: "javascript:alert(1)"}, false},
		{CustomApp{Name: "x", Icon: "data:image/svg+xml,<svg>"}, false},
		{CustomApp{Name: "x", Icon: "https://user:pw@example.com/i.png"}, false},
		{CustomApp{Name: "x", Icon: "https://example.com/a b.png"}, false},
		{CustomApp{Name: "x", WebPort: 70000}, false},
		{CustomApp{Name: "x", WebPort: -1}, false},
		{CustomApp{Name: "x", Description: strings.Repeat("d", 301)}, false},
	} {
		c := tc.c
		if err := c.Validate(); (err == nil) != tc.ok {
			t.Errorf("%+v: %v", tc.c, err)
		}
	}
}

func TestCustomAppFiles(t *testing.T) {
	prev := StacksDir
	StacksDir = t.TempDir()
	t.Cleanup(func() { StacksDir = prev })
	if err := WriteCustom("web", CustomApp{Name: "Web"}); err == nil {
		t.Error("custom app saved without a stack")
	}
	_ = os.MkdirAll(filepath.Join(StacksDir, "web"), 0o750)
	_ = os.WriteFile(filepath.Join(StacksDir, "web", "compose.yaml"), []byte("services: {}\n"), 0o640)
	if err := WriteCustom("web", CustomApp{Name: "Web", WebPort: 8099}); err != nil {
		t.Fatal(err)
	}
	if c := ReadCustom("web"); c == nil || c.Name != "Web" || AppWebPort("web") != "8099" {
		t.Errorf("read back %+v port %q", c, AppWebPort("web"))
	}
	if ReadCustom("../web") != nil || AppWebPort("../web") != "" {
		t.Error("invalid stack name accepted")
	}
	if err := RemoveCustom("web"); err != nil || ReadCustom("web") != nil {
		t.Errorf("remove: %v", err)
	}
	// A catalog app cannot become a custom app.
	_ = os.WriteFile(filepath.Join(StacksDir, "web", ".strata-app"), []byte("memos"), 0o640)
	_ = os.WriteFile(filepath.Join(StacksDir, "web", ".env"), []byte("WEB_PORT=\"5231\"\n"), 0o600)
	if err := WriteCustom("web", CustomApp{Name: "Web"}); err == nil {
		t.Error("catalog app saved as a custom app")
	}
	if AppWebPort("web") != "5231" {
		t.Errorf("catalog app port %q", AppWebPort("web"))
	}
}
