package confbackup

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"strataos/internal/firewall"
	"strataos/internal/network"
	"strataos/internal/store"
)

// source12 is testSource with what 0.11 and 0.12 added.
func source12() Source {
	src := testSource()
	st := &src.State
	widgets := []string{"status", "storage"}
	st.Users[1].Desktop = map[string][2]int{"files": {0, 1}}
	st.Users[1].DesktopWidgets = &widgets
	st.Users[1].Accent, st.Users[1].Lang, st.Users[1].Wallpaper = "#ff8800", "fr", "custom"
	st.Users[1].S3Keys = []store.S3Key{{Access: "STRAAAAABBBBCCCC", Secret: "s3-secret-key-of-alice-0000", Name: "laptop", Created: src.Now}}
	st.Users[1].SecurityKeys = []store.Passkey{{ID: "seckey-credential-id", Name: "YubiKey"}}
	st.Firewall = &firewall.Config{
		Host:   firewall.Host{Enabled: true, PolicyIn: "drop", Rules: []firewall.Rule{{Dir: "in", Action: "accept", Source: "lan"}}},
		IPSets: []firewall.IPSet{{Name: "office", Entries: []string{"198.51.100.0/24"}}},
		Groups: []firewall.Group{{Name: "web", Rules: []firewall.Rule{{Dir: "in", Action: "accept", Proto: "tcp", Ports: "80,443"}}}},
		Guests: []firewall.Guest{{Kind: "vm", Name: "web1", Enabled: true, Rules: []firewall.Rule{{Dir: "in", Action: "accept", Group: "web"}}}},
	}
	st.OIDC = []*store.OIDCProvider{{ID: "authentik", Name: "Authentik", Issuer: "https://id.example.com/", ClientID: "strata", ClientSecret: "oidc-client-secret", Enabled: true, GroupMap: map[string]string{"admins": "famille"}}}
	st.TwoFactor = store.TwoFactorPolicy{Require: true, RequireKey: true, GraceDays: 3, Since: src.Now}
	st.S3 = store.S3Service{Enabled: true, Port: 7380, Buckets: []store.S3Bucket{{Name: "photos", Share: "Photos"}}, RootAccess: "ROOTACCESS", RootSecret: "root-secret-of-the-gateway"}
	st.Datastores = []*store.Datastore{{ID: "d1", Name: "Repo", DestType: "local", DestPath: "/srv/strata/main/repo", Password: "datastore-pass",
		Verifies: []store.VerifyRun{{Status: "success"}}, Stats: &store.DatastoreStats{Snapshots: 4}, VerifyNext: 2}}
	st.Backups[0].Datastore = "d1"
	st.Photos.Smart = store.PhotoSmart{Enabled: true, NoFaces: true}
	st.Settings.NotifyLang = "de"
	st.S3.Buckets = append(st.S3.Buckets, store.S3Bucket{Name: "archive", Share: "Media", Versioning: "enabled", Lock: true, LockMode: "GOVERNANCE", LockDays: 30})
	src.Network = &network.Config{Virtual: []network.VNet{
		{ID: "iot", Kind: "nat", Bridge: "vn0", Address: "10.77.0.1/24", DHCP: true},
		// Shared from here: the tunnel side is filled by StrataOS.
		{ID: "lab", Kind: "vxlan", Bridge: "vn1", VNI: 4100, Subnet: "10.78.0.0/24", Members: []string{"fleet:m1", "cluster:n2"}, Encrypt: true,
			Peers: []string{"fd00:1::2"}, Local: "fd00:1::1", WG: &network.VNetWG{Port: 51900, Address: "fd00:1::1/64", Peers: []network.WGPeer{{PublicKey: "pub-of-nas2", Endpoint: "192.0.2.2:51900", Address: "fd00:1::2"}}}},
		// Received from another NAS, which manages it.
		{ID: "remote", Kind: "vxlan", Bridge: "vn2", VNI: 4200, Peers: []string{"192.0.2.9"}, Origin: "nas9", OriginKey: "fleet:abc"},
	}}
	return src
}

func doc12(t *testing.T, pass string) *Document {
	t.Helper()
	cfg, sec := Build(source12())
	d := Document{Format: Format, Version: Version, StrataOS: "0.12.0", Created: time.Now(), Hostname: "nas1", Config: cfg}
	if pass != "" {
		s, err := Seal(sec, pass)
		if err != nil {
			t.Fatal(err)
		}
		d.Secrets = s
	}
	b, _ := json.Marshal(d)
	out, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSections012(t *testing.T) {
	plainDoc, _ := json.Marshal(doc12(t, ""))
	for _, s := range []string{"oidc-client-secret", "s3-secret-key-of-alice-0000", "datastore-pass", "ROOTACCESS", "root-secret-of-the-gateway", "seckey-credential-id", `"snapshots"`, `"custom"`} {
		if strings.Contains(string(plainDoc), s) {
			t.Errorf("plain export contains %q", s)
		}
	}
	for _, s := range []string{`"remote"`, `"pub-of-nas2"`, `"192.0.2.9"`, `fd00:1::`} {
		if strings.Contains(string(plainDoc), s) {
			t.Errorf("plain export contains %q (received network or tunnel side of a shared one)", s)
		}
	}
	for _, s := range []string{`"lab"`, `"members":["fleet:m1","cluster:n2"]`, `"encrypt":true`, `"versioning":"enabled"`, `"lock":true`, `"lockMode":"GOVERNANCE"`, `"lockDays":30`} {
		if !strings.Contains(string(plainDoc), s) {
			t.Errorf("plain export lacks %s", s)
		}
	}
	if src := source12(); func() bool {
		Build(src)
		return len(src.Network.Virtual) != 3 || src.Network.Virtual[1].WG == nil || src.Network.Virtual[1].Local == ""
	}() {
		t.Errorf("the export changed the source")
	}
	for _, s := range []string{`"office"`, `"web1"`, `"authentik"`, `"requireKey":true`, `"photos"`, `"STRAAAAABBBBCCCC"`, `"Repo"`, `"noFaces":true`, `"#ff8800"`, `"notifyLang":"de"`, `"iot"`} {
		if !strings.Contains(string(plainDoc), s) {
			t.Errorf("plain export lacks %s", s)
		}
	}

	d := doc12(t, "a long passphrase")
	plain, full, sec, err := d.Prepare("a long passphrase", nil, nil)
	if err != nil || sec == nil {
		t.Fatal(err)
	}
	if full.OIDC[0].ClientSecret != "oidc-client-secret" || plain.OIDC[0].ClientSecret != Masked {
		t.Errorf("oidc secret: %q %q", full.OIDC[0].ClientSecret, plain.OIDC[0].ClientSecret)
	}
	if full.S3.Keys[0].Secret != "s3-secret-key-of-alice-0000" || plain.S3.Keys[0].Secret != Masked {
		t.Errorf("s3 key: %+v", full.S3.Keys[0])
	}
	if full.Datastores[0].Password != "datastore-pass" || plain.Datastores[0].Password != Masked || full.Datastores[0].Stats != nil || full.Datastores[0].Verifies != nil {
		t.Errorf("datastore: %+v", full.Datastores[0])
	}
	if full.TwoFactor == nil || !full.TwoFactor.RequireKey || full.TwoFactor.GraceDays != 3 {
		t.Errorf("two-factor policy: %+v", full.TwoFactor)
	}

	// On a new NAS: everything is to create.
	env := Env{Self: "admin", Secrets: true, OnVolume: onVolumes("/srv/strata/main"), Exists: func(string) bool { return true }, Interfaces: []string{"eth0"}}
	empty := Config{Users: []User{{Username: "admin"}}, Settings: &store.Settings{}}
	secs := Plan(plain, &empty, env)
	for id, key := range map[string]string{"datastores": "d1", "oidc": "authentik", "s3": "key:STRAAAAABBBBCCCC", "firewall": "group:web", "desktop": "alice"} {
		if it, ok := items(secs, id)[key]; !ok || (it.Action != Create && it.Action != Update) {
			t.Errorf("%s/%s: %+v", id, key, it)
		}
	}
	for _, id := range []string{"twoFactor", "photoSmart"} {
		if it := items(secs, id)[id]; it.Action != Update {
			t.Errorf("%s: %+v", id, it)
		}
	}
	if it := items(secs, "firewall")["host"]; it.Action != Update || !strings.Contains(it.Detail, "trial") {
		t.Errorf("NAS firewall: %+v", it)
	}
	if it := items(secs, "network")["vnet:iot"]; it.Action != Report {
		t.Errorf("virtual network: %+v (sections %+v)", it, secs)
	}
	if it := items(secs, "network")["vnet:lab"]; it.Action != Report || !strings.Contains(it.Detail, "sent again") {
		t.Errorf("shared virtual network: %+v", it)
	}
	if _, ok := items(secs, "network")["vnet:remote"]; ok {
		t.Errorf("received virtual network planned")
	}
	if it := items(secs, "settings")["settings"]; !slicesHas(it.Changes, "notifyLang") {
		t.Errorf("settings changes: %+v", it)
	}
	// The order: repositories before the tasks that use them.
	if a, b := sectionIndex("datastores"), sectionIndex("backups"); a < 0 || a > b {
		t.Errorf("datastores (%d) must come before backups (%d)", a, b)
	}

	// Round trip: importing into the NAS that exported changes nothing.
	cur, _ := Build(source12())
	for _, s := range Plan(plain, &cur, env) {
		for _, it := range s.Items {
			if it.Action == Create || it.Action == Update {
				t.Errorf("round trip: %s/%s is %s (%v)", s.ID, it.Key, it.Action, it.Changes)
			}
		}
	}

	// Buckets that exist only here are kept by the import: still nothing to do.
	cur.S3.Buckets = append(cur.S3.Buckets, store.S3Bucket{Name: "local", Share: "Local"})
	if it := items(Plan(plain, &cur, env), "s3")["settings"]; it.Action != Same {
		t.Errorf("s3 with a bucket of this NAS: %+v", it)
	}

	// Without the secrets: keys and repositories that need them are skipped.
	plain2, _, sec2, err := doc12(t, "").Prepare("", nil, nil)
	if err != nil || sec2 != nil {
		t.Fatal(err)
	}
	env.Secrets = false
	secs = Plan(plain2, &empty, env)
	if it := items(secs, "s3")["key:STRAAAAABBBBCCCC"]; it.Action != Skip {
		t.Errorf("s3 key without secrets: %+v", it)
	}
	if it := items(secs, "datastores")["d1"]; it.Action != Skip {
		t.Errorf("datastore without secrets: %+v", it)
	}
	if it := items(secs, "oidc")["authentik"]; it.Action != Create || !strings.Contains(it.Detail, "disabled") {
		t.Errorf("oidc without secrets: %+v", it)
	}

	// A file from 0.10 has none of these sections: nothing is planned for them.
	old, _, _, _ := mustParse(t, export(t, "")).Prepare("", nil, nil)
	for _, s := range Plan(old, &cur, env) {
		if slicesHas([]string{"firewall", "oidc", "twoFactor", "s3", "datastores", "photoSmart", "desktop"}, s.ID) {
			t.Errorf("old file plans section %s: %+v", s.ID, s.Items)
		}
	}

	// Volume mapping reaches the new paths.
	m, _, _, _ := doc12(t, "").Prepare("", map[string]string{"/srv/strata/main": "/srv/strata/data"}, nil)
	if m.Datastores[0].DestPath != "/srv/strata/data/repo" {
		t.Errorf("mapping: %q", m.Datastores[0].DestPath)
	}
}

func mustParse(t *testing.T, b []byte) *Document {
	t.Helper()
	d, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func slicesHas(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func sectionIndex(id string) int {
	for i, s := range Sections {
		if s == id {
			return i
		}
	}
	return -1
}
