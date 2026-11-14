package confbackup

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func TestSSOSection(t *testing.T) {
	src := testSource()
	src.State.Users[1].SSOSubject = "subject-of-alice"
	src.State.SSO = store.SSO{Enabled: true, Issuer: "https://nas.example.com", Clients: []*store.SSOClient{
		{ID: "grafana-a1b2c3", Name: "Grafana", Kind: "oidc", App: "grafana", Secret: "sso-client-secret", RedirectURIs: []string{"https://g.example.com/login/generic_oauth"}, Groups: []string{"famille"}},
		{ID: "fwd-d1", Name: "kuma.example.com", Kind: "forward", Domain: "d1", Public: true, AllUsers: true},
	}}
	cfg, sec := Build(src)
	b, _ := json.Marshal(cfg)
	if strings.Contains(string(b), "sso-client-secret") || !strings.Contains(string(b), `"famille"`) || strings.Contains(string(b), "fwd-d1") {
		t.Fatalf("export: %s", b)
	}
	if sec.SSO["grafana-a1b2c3"] != "sso-client-secret" || cfg.SSO.Subjects[src.State.Users[1].Username] != "subject-of-alice" {
		t.Fatalf("secrets: %v %v", sec.SSO, cfg.SSO.Subjects)
	}
	s, err := Seal(sec, "a long passphrase")
	if err != nil {
		t.Fatal(err)
	}
	d := Document{Format: Format, Version: Version, StrataOS: "0.12.0", Created: time.Now(), Hostname: "nas1", Config: cfg, Secrets: s}
	raw, _ := json.Marshal(d)
	doc, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	plain, full, _, err := doc.Prepare("a long passphrase", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if full.SSO.Clients[0].Secret != "sso-client-secret" || plain.SSO.Clients[0].Secret != Masked {
		t.Fatalf("unmask: %q %q", full.SSO.Clients[0].Secret, plain.SSO.Clients[0].Secret)
	}
	env := Env{Self: "admin", Secrets: true, OnVolume: onVolumes("/srv/strata/main"), Exists: func(string) bool { return true }}
	secs := Plan(plain, &Config{Users: []User{{Username: "admin"}}, Settings: &store.Settings{}}, env)
	if it := items(secs, "sso")["client:grafana-a1b2c3"]; it.Action != Create {
		t.Fatalf("plan: %+v", it)
	}
	if it := items(secs, "sso")["settings"]; it.Action != Update {
		t.Fatalf("plan settings: %+v", it)
	}
	// The same configuration plans nothing.
	secs = Plan(plain, plain, env)
	for k, it := range items(secs, "sso") {
		if it.Action != Same {
			t.Errorf("%s: %+v", k, it)
		}
	}
	// Without the secrets the client is created disabled.
	env.Secrets = false
	secs = Plan(plain, &Config{Users: []User{{Username: "admin"}}, Settings: &store.Settings{}}, env)
	if it := items(secs, "sso")["client:grafana-a1b2c3"]; !strings.Contains(it.Detail, "disabled") {
		t.Fatalf("without secrets: %+v", it)
	}
}
