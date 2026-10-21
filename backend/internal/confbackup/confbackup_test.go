package confbackup

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"strataos/internal/guard"
	"strataos/internal/store"
)

func testSource() Source {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	st := store.State{
		Users: []*store.User{
			{Username: "admin", Role: "admin", PasswordHash: "$2a$12$adminhash"},
			{Username: "alice", FullName: "Alice", Role: "user", PasswordHash: "$2a$12$alicehash", TOTPSecret: "TOTPKEY", RecoveryCodes: []string{"r1"}},
		},
		Volumes: []*store.Volume{{ID: "v1", Name: "main", FSType: "ext4", MountPoint: "/srv/strata/main"}, {ID: "v2", Name: "fast", FSType: "btrfs", MountPoint: "/srv/strata/fast"}},
		Shares: []*store.Share{
			{Name: "Photos", Path: "/srv/strata/main/Photos", SMB: true, Permissions: map[string]string{"@famille": "rw"}, Used: 1234, QuotaFull: true, Frozen: true},
			{Name: "Fast", Path: "/srv/strata/fast/Fast", SMB: true},
		},
		Schedules: []*store.Schedule{{ID: "s1", Name: "Snap fast", Type: "snapshot", Cron: "0 * * * *", TargetKind: "btrfs", Target: "v2", KeepCount: 5, History: []store.ScheduleRun{{Status: "success"}}}},
		Backups:   []*store.BackupTask{{ID: "b1", Name: "Offsite", Kind: "restic", Sources: []string{"/srv/strata/main/Photos"}, DestType: "s3", DestPath: "bucket/x", AccessKey: "AK", SecretKey: "SK", Password: "repo-pass"}},
		Clouds:    []*store.CloudSync{{ID: "c1", Name: "Drive", Provider: "s3", LocalPath: "/srv/strata/main/Photos", Params: map[string]string{"region": "eu"}, Secrets: map[string]string{"secret_access_key": "CS"}, Synced: true}},
		VPN: store.VPN{Port: 51820, Subnet: "10.8.0.0/24", PrivateKey: "SRVPRIV", PublicKey: "SRVPUB", Peers: []store.VPNPeer{{ID: "p1", Name: "phone", Mode: "nas", IP: "10.8.0.2", PublicKey: "PEERPUB", PrivateKey: "PEERPRIV", PresharedKey: "PSK"}},
			Links:  []store.VPNLink{{ID: "l1", Name: "Office", Iface: "strata-l1", PrivateKey: "LINKPRIV", PresharedKey: "LINKPSK", PeerPublicKey: "LPUB"}},
			Client: store.VPNClient{Enabled: true, PrivateKey: "CLIPRIV", PeerPublicKey: "CPUB", Scope: "apps", Apps: []string{"qbittorrent"}, Detached: map[string][]string{"qbittorrent": {"bridge"}}}},
		DDNS: store.DDNS{Provider: "duckdns", Hostname: "nas", Token: "DDTOK", LastIP: "1.2.3.4"},
		Guard: store.Guard{Config: guard.Config{Enabled: true, MaxFailures: 5}, Blocks: []guard.Block{
			{IP: "203.0.113.9", Until: now.Add(time.Hour), Manual: true},
			{IP: "203.0.113.10", Until: now.Add(time.Hour)},                // automatic: runtime
			{IP: "203.0.113.11", Until: now.Add(-time.Hour), Manual: true}, // over
		}},
		Ransom:       store.Ransomware{Enabled: true, Threshold: 100, Blocked: []string{"bob"}},
		Events:       []store.Event{{Message: "secret event"}},
		Settings:     store.Settings{Workgroup: "HOME", SMTPHost: "smtp.example.com", SMTPPassword: "smtp-pass", TelegramToken: "tg"},
		ConfigBackup: store.ConfigBackup{Enabled: true, Folder: "/srv/strata/main/cfg", Keep: 7, Secrets: true, Passphrase: "auto-passphrase", LastResult: "OK"},
	}
	return Source{
		State:  st,
		System: &System{Hostname: "nas1", Timezone: "Europe/Paris"},
		Groups: []Group{{Name: "famille", GID: 1001, Members: []string{"alice"}}},
		Linux: map[string]LinuxUser{
			"admin": {UID: 1000, GID: 1000, Shadow: "$y$j9T$adminshadow"},
			"alice": {UID: 1002, GID: 1002, Groups: []string{"famille"}, Shadow: "$y$j9T$aliceshadow"},
		},
		SMB:    map[string]string{"alice": "alice:1002:XXXX:NTHASH:[U          ]:LCT-00000000:"},
		Quotas: []Quota{{User: "alice", Kind: "ext4", MountPoint: "/srv/strata/main", Limit: 1 << 30}},
		Stacks: []Stack{{Name: "nextcloud", App: "nextcloud", Compose: "services: {}\n", Env: "WEB_PORT=8080\nDATA_DIR=/srv/strata/main/apps/nc\nDB_PASSWORD=s3cret\n"}},
		Now:    now,
	}
}

func export(t *testing.T, pass string) []byte {
	t.Helper()
	cfg, sec := Build(testSource())
	d := Document{Format: Format, Version: Version, StrataOS: "0.6.0", Created: time.Now(), Hostname: "nas1", Config: cfg}
	if pass != "" {
		s, err := Seal(sec, pass)
		if err != nil {
			t.Fatal(err)
		}
		d.Secrets = s
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPlainExportHasNoSecretsNorRuntime(t *testing.T) {
	b := string(export(t, ""))
	for _, s := range []string{"smtp-pass", "repo-pass", "SK", "CS", "SRVPRIV", "PEERPRIV", "LINKPRIV", "LINKPSK", "CLIPRIV", "PSK", "DDTOK", "s3cret", "adminhash", "aliceshadow", "NTHASH", "TOTPKEY", "auto-passphrase", "secret event", "1.2.3.4", `"bob"`, "203.0.113.10", "203.0.113.11", `"status"`} {
		if strings.Contains(b, s) {
			t.Errorf("plain export contains %q", s)
		}
	}
	for _, s := range []string{`"workgroup": "HOME"`, "203.0.113.9", "PEERPUB", `"accessKey": "AK"`, "DB_PASSWORD=" + Masked, "DATA_DIR=/srv/strata/main/apps/nc", `"uid": 1002`, `"famille"`} {
		if !strings.Contains(b, s) {
			t.Errorf("plain export lacks %q", s)
		}
	}
	var d Document
	_ = json.Unmarshal([]byte(b), &d)
	if sh := d.Config.Shares[0]; sh.Used != 0 || sh.QuotaFull || sh.Frozen {
		t.Errorf("runtime share fields exported: %+v", sh)
	}
}

func TestSealOpen(t *testing.T) {
	type v struct{ A string }
	s, err := Seal(v{"hello"}, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	var out v
	if err := s.Open("correct horse", &out); err != nil || out.A != "hello" {
		t.Fatalf("open: %v %+v", err, out)
	}
	if err := s.Open("wrong horse!", &out); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	s2, _ := Seal(v{"hello"}, "correct horse")
	if string(s2.Salt) == string(s.Salt) || string(s2.Nonce) == string(s.Nonce) {
		t.Error("salt and nonce must be random")
	}
	if _, err := Seal(v{}, "short"); err == nil {
		t.Error("short passphrase accepted")
	}
	s.Memory = 4 << 20 // a crafted file asking for 4 GiB
	if err := s.Open("correct horse", &out); err == nil || errors.Is(err, ErrPassphrase) {
		t.Errorf("abusive parameters accepted: %v", err)
	}
	s.Memory = kdfMemory
	s.Data[0] ^= 1
	if err := s.Open("correct horse", &out); !errors.Is(err, ErrPassphrase) {
		t.Errorf("tampered data: %v", err)
	}
}

func TestParseVersions(t *testing.T) {
	if _, err := Parse([]byte(`{"format":"other","version":1}`)); !errors.Is(err, ErrFormat) {
		t.Errorf("other format: %v", err)
	}
	if _, err := Parse([]byte(`not json`)); !errors.Is(err, ErrFormat) {
		t.Errorf("garbage: %v", err)
	}
	_, err := Parse([]byte(`{"format":"strataos-config","version":99,"strataos":"9.0"}`))
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("newer version: %v", err)
	}
	if _, err := Parse(export(t, "")); err != nil {
		t.Errorf("current version: %v", err)
	}
}

func onVolumes(mounts ...string) func(string) bool {
	return func(p string) bool {
		return slices.ContainsFunc(mounts, func(m string) bool { return p == m || strings.HasPrefix(p, m+"/") })
	}
}

func items(secs []Section, id string) map[string]Item {
	out := map[string]Item{}
	for _, s := range secs {
		if s.ID == id {
			for _, it := range s.Items {
				out[it.Key] = it
			}
		}
	}
	return out
}

func TestRoundTrip(t *testing.T) {
	d, err := Parse(export(t, "a long passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.Prepare("wrong passphrase", nil, nil); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	vid := func(m string) string {
		if m == "/srv/strata/fast" {
			return "local-fast"
		}
		return ""
	}
	plain, full, sec, err := d.Prepare("a long passphrase", nil, vid)
	if err != nil || sec == nil {
		t.Fatal(err)
	}
	// Secrets are back in the configuration to apply, masked in the one compared.
	if full.Settings.SMTPPassword != "smtp-pass" || plain.Settings.SMTPPassword != Masked {
		t.Errorf("settings secrets: %q %q", full.Settings.SMTPPassword, plain.Settings.SMTPPassword)
	}
	if full.Backups[0].Password != "repo-pass" || full.Backups[0].SecretKey != "SK" || full.CloudSyncs[0].Secrets["secret_access_key"] != "CS" {
		t.Errorf("task secrets: %+v %+v", full.Backups[0], full.CloudSyncs[0].Secrets)
	}
	if full.VPN.Links[0].PrivateKey != "LINKPRIV" || full.VPN.Links[0].PresharedKey != "LINKPSK" || full.VPN.Client.PrivateKey != "CLIPRIV" || full.VPN.Client.Detached != nil {
		t.Errorf("vpn link/client secrets: %+v %+v", full.VPN.Links, full.VPN.Client)
	}
	if full.VPN.PrivateKey != "SRVPRIV" || full.VPN.Peers[0].PrivateKey != "PEERPRIV" || full.DDNS.Token != "DDTOK" || full.ConfigBackup.Passphrase != "auto-passphrase" {
		t.Errorf("vpn/ddns secrets: %+v %+v", full.VPN, full.DDNS)
	}
	if !strings.Contains(full.Stacks[0].Env, "DB_PASSWORD=s3cret") {
		t.Errorf("stack env: %q", full.Stacks[0].Env)
	}
	if u := sec.Users["alice"]; u.Shadow != "$y$j9T$aliceshadow" || !strings.Contains(u.SMB, "NTHASH") || u.TOTPSecret != "TOTPKEY" {
		t.Errorf("user secrets: %+v", u)
	}
	if full.Schedules[0].Target != "local-fast" {
		t.Errorf("btrfs target not resolved: %q", full.Schedules[0].Target)
	}

	// On an empty NAS everything is created (the admin importing excepted).
	env := Env{Self: "admin", Secrets: true, OnVolume: onVolumes("/srv/strata/main", "/srv/strata/fast"), QuotaOn: func(string) bool { return true }, Docker: true, Exists: func(string) bool { return true }}
	empty := Config{System: &System{Hostname: "fresh"}}
	plan := Plan(plain, &empty, env)
	if it := items(plan, "users")["admin"]; it.Action != Skip {
		t.Errorf("own account: %+v", it)
	}
	if it := items(plan, "users")["alice"]; it.Action != Create || it.Detail != "Password and Samba password restored" {
		t.Errorf("alice: %+v", it)
	}
	for _, sec := range []string{"shares", "schedules", "backups", "cloudSyncs", "stacks", "groups", "quotas"} {
		for k, it := range items(plan, sec) {
			if it.Action != Create {
				t.Errorf("%s/%s: %+v", sec, k, it)
			}
		}
	}
	if it := items(plan, "guard")["203.0.113.9"]; it.Action != Create {
		t.Errorf("manual block: %+v", it)
	}
	if it := items(plan, "system")["hostname"]; it.Action != Update {
		t.Errorf("hostname: %+v", it)
	}

	// Imported again over the same configuration: nothing to do.
	cur, _ := Build(testSource())
	for _, s := range Plan(plain, &cur, env) {
		for _, it := range s.Items {
			if it.Action == Create || it.Action == Update {
				// Btrfs targets are local IDs here and old IDs in cur.
				if s.ID == "schedules" {
					continue
				}
				t.Errorf("%s/%s should be unchanged: %+v", s.ID, it.Key, it)
			}
		}
	}

	// A changed share and a deleted schedule are detected.
	cur.Shares[0].Guest = true
	cur.Schedules = nil
	plan = Plan(plain, &cur, env)
	if it := items(plan, "shares")["Photos"]; it.Action != Update || !slices.Contains(it.Changes, "guest") {
		t.Errorf("changed share: %+v", it)
	}
	if it := items(plan, "schedules")["s1"]; it.Action != Create {
		t.Errorf("deleted schedule: %+v", it)
	}
}

func TestWithoutSecrets(t *testing.T) {
	d, _ := Parse(export(t, ""))
	plain, full, sec, err := d.Prepare("", nil, nil)
	if err != nil || sec != nil {
		t.Fatal(err, sec)
	}
	if full.Backups[0].Password != Masked {
		t.Errorf("a missing secret stays masked: %q", full.Backups[0].Password)
	}
	env := Env{Self: "admin", OnVolume: onVolumes("/srv/strata/main", "/srv/strata/fast"), Docker: true}
	plan := Plan(plain, &Config{}, env)
	if it := items(plan, "users")["alice"]; it.Detail != "Created disabled: set a password to enable it" {
		t.Errorf("alice: %+v", it)
	}
	if it := items(plan, "cloudSyncs")["c1"]; it.Action != Skip {
		t.Errorf("cloud without credentials: %+v", it)
	}
	if it := items(plan, "backups")["b1"]; it.Action != Create || !strings.Contains(it.Detail, "disabled") {
		t.Errorf("backup without password: %+v", it)
	}
	if it := items(plan, "vpn")["p1"]; it.Action != Skip {
		t.Errorf("vpn device without keys: %+v", it)
	}
	// Unknown secrets of an update keep the current values.
	if got := MergeEnv("A=1\nDB_PASSWORD="+Masked+"\nNEW_TOKEN="+Masked, "DB_PASSWORD=here\n"); got != "A=1\nDB_PASSWORD=here\nNEW_TOKEN=" {
		t.Errorf("MergeEnv: %q", got)
	}
}

func TestVolumeMapping(t *testing.T) {
	d, _ := Parse(export(t, ""))
	m := map[string]string{"/srv/strata/main": "/srv/strata/big"}
	vid := func(string) string { return "" } // no btrfs volume here
	plain, _, _, err := d.Prepare("", m, vid)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Shares[0].Path != "/srv/strata/big/Photos" || plain.Backups[0].Sources[0] != "/srv/strata/big/Photos" || plain.CloudSyncs[0].LocalPath != "/srv/strata/big/Photos" || plain.Quotas[0].MountPoint != "/srv/strata/big" {
		t.Errorf("paths not mapped: %+v", plain.Shares[0])
	}
	if !strings.Contains(plain.Stacks[0].Env, "DATA_DIR=/srv/strata/big/apps/nc") {
		t.Errorf("app folder not mapped: %q", plain.Stacks[0].Env)
	}
	env := Env{Self: "admin", OnVolume: onVolumes("/srv/strata/big")}
	plan := Plan(plain, &Config{}, env)
	if it := items(plan, "shares")["Photos"]; it.Action != Create {
		t.Errorf("mapped share: %+v", it)
	}
	if it := items(plan, "shares")["Fast"]; it.Action != Skip || !strings.Contains(it.Detail, "/srv/strata/fast/Fast") {
		t.Errorf("share on a missing volume: %+v", it)
	}
	if it := items(plan, "schedules")["s1"]; it.Action != Skip {
		t.Errorf("task on a missing volume: %+v", it)
	}
	if it := items(plan, "volumes")["/srv/strata/main"]; it.Detail != "Mapped to /srv/strata/big" {
		t.Errorf("volume report: %+v", it)
	}
	if it := items(plan, "volumes")["/srv/strata/fast"]; it.Action != Report || !strings.HasPrefix(it.Detail, "Not found") {
		t.Errorf("volume report: %+v", it)
	}
	if _, _, _, err := d.Prepare("", map[string]string{"/srv/strata/main": "/etc"}, nil); err == nil {
		t.Error("mapping outside the storage accepted")
	}
	if got := MapPath("/srv/strata/main2/x", m); got != "/srv/strata/main2/x" {
		t.Errorf("prefix of another volume mapped: %q", got)
	}
}

func TestRedactEnv(t *testing.T) {
	in := "# comment with password=x\nWEB_PORT=80\nAPI_KEY=abc\nadmin_pwd=1\nEMPTY_PASSWORD=\n"
	want := "# comment with password=x\nWEB_PORT=80\nAPI_KEY=" + Masked + "\nadmin_pwd=" + Masked + "\nEMPTY_PASSWORD=\n"
	if got := RedactEnv(in); got != want {
		t.Errorf("RedactEnv:\n%q\nwant\n%q", got, want)
	}
}
