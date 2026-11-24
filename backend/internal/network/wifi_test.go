package network

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWirelessDetection(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"wlan0", "wlp3s0"} {
		if err := os.MkdirAll(filepath.Join(dir, n, "wireless"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "eth0"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := SysNetDir
	SysNetDir = dir
	t.Cleanup(func() { SysNetDir = old })
	got := Wireless()
	if len(got) != 2 || got[0] != "wlan0" || got[1] != "wlp3s0" {
		t.Fatalf("Wireless() = %v", got)
	}
}

func TestValidateWiFi(t *testing.T) {
	wl := []string{"wlan0"}
	ok := Config{WiFi: []WiFi{{Name: "wlan0", SSID: "Home", PSK: "password123", Enabled: true}}}
	if err := ValidateWiFi(&ok, wl); err != nil {
		t.Fatalf("valid: %v", err)
	}
	bad := []Config{
		{WiFi: []WiFi{{Name: "eth0", SSID: "x", Enabled: true}}},                                             // not wireless
		{WiFi: []WiFi{{Name: "wlan0", SSID: "", Enabled: true}}},                                             // no ssid
		{WiFi: []WiFi{{Name: "wlan0", SSID: "x", PSK: "short", Enabled: true}}},                              // psk too short
		{WiFi: []WiFi{{Name: "wlan0", SSID: "x\nkey_mgmt=NONE", Enabled: true}}},                             // injection
		{WiFi: []WiFi{{Name: "wlan0", SSID: "x", Enabled: true}, {Name: "wlan0", SSID: "y", Enabled: true}}}, // twice
	}
	for i, c := range bad {
		if err := ValidateWiFi(&c, wl); err == nil {
			t.Errorf("case %d should fail", i)
		}
	}
	// An open network needs no psk; a 64-hex key is valid; disabled entries are not checked.
	o := Config{WiFi: []WiFi{{Name: "wlan0", SSID: "Cafe", Enabled: true}}}
	if err := ValidateWiFi(&o, wl); err != nil {
		t.Errorf("open network: %v", err)
	}
	pmk := Config{WiFi: []WiFi{{Name: "wlan0", SSID: "x", PSK: strings.Repeat("ab", 32), Enabled: true}}}
	if err := ValidateWiFi(&pmk, wl); err != nil {
		t.Errorf("64-hex psk: %v", err)
	}
	off := Config{WiFi: []WiFi{{Name: "wlan0", Enabled: false}}}
	if err := ValidateWiFi(&off, wl); err != nil {
		t.Errorf("disabled entry: %v", err)
	}
}

func TestWPAConf(t *testing.T) {
	c := wpaConf(WiFi{Name: "wlan0", SSID: "Home \"Net\"", PSK: "pass word1", Hidden: true, Enabled: true})
	if !strings.Contains(c, "ssid="+hex.EncodeToString([]byte("Home \"Net\""))) || !strings.Contains(c, "psk="+wpaPSK("pass word1", "Home \"Net\"")+"\n") || !strings.Contains(c, "scan_ssid=1") {
		t.Errorf("conf:\n%s", c)
	}
	pmk := strings.Repeat("ab", 32)
	c = wpaConf(WiFi{Name: "wlan0", SSID: "plain", PSK: pmk, Enabled: true})
	if !strings.Contains(c, "psk="+pmk+"\n") {
		t.Errorf("raw psk should be unquoted:\n%s", c)
	}
	c = wpaConf(WiFi{Name: "wlan0", SSID: "Cafe", Enabled: true})
	if !strings.Contains(c, "key_mgmt=NONE") {
		t.Errorf("open network:\n%s", c)
	}
}

func TestWriteWPA(t *testing.T) {
	dir := t.TempDir()
	old := WPAConfDir
	WPAConfDir = dir
	t.Cleanup(func() { WPAConfDir = old })
	// A stale conf of a now-disabled link must be removed.
	if err := os.WriteFile(filepath.Join(dir, wpaPrefix+"wlan1.conf"), []byte(wpaMarker+"old"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A file of 0.15.1 (strata-<link>.conf) goes; one written by hand stays.
	os.WriteFile(filepath.Join(dir, "strata-wlan0.conf"), []byte("old"), 0o600)
	os.WriteFile(filepath.Join(dir, wpaPrefix+"wlan9.conf"), []byte("network={}"), 0o600)
	c := Config{WiFi: []WiFi{
		{Name: "wlan0", SSID: "Home", PSK: "password123", Enabled: true},
		{Name: "wlan1", SSID: "Old", PSK: "password123", Enabled: false},
	}}
	if err := writeWPA(c); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, wpaPrefix+"wlan0.conf"))
	if err != nil {
		t.Fatalf("wlan0 conf missing: %v", err)
	}
	fi, _ := os.Stat(filepath.Join(dir, wpaPrefix+"wlan0.conf"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("conf permissions = %o, want 600", fi.Mode().Perm())
	}
	if !strings.Contains(string(b), `ssid="Home"`) || !strings.Contains(string(b), "psk="+wpaPSK("password123", "Home")+"\n") {
		t.Errorf("conf:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, wpaPrefix+"wlan1.conf")); !os.IsNotExist(err) {
		t.Errorf("disabled link conf should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "strata-wlan0.conf")); !os.IsNotExist(err) {
		t.Errorf("0.15.1 file should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, wpaPrefix+"wlan9.conf")); err != nil {
		t.Errorf("hand-written file removed")
	}
}

// IEEE 802.11i-2004, H.4.1 test vectors (what wpa_passphrase prints).
func TestWPAPSKVectors(t *testing.T) {
	for _, v := range []struct{ pass, ssid, psk string }{
		{"password", "IEEE", "f42c6fc52df0ebef9ebb4b90b38a5f902e83fe1b135a70e23aed762e9710a12e"},
		{"ThisIsAPassword", "ThisIsASSID", "0dc0d6eb90555ed6419756b9a15ec3e3209b63df707dd508d14581f8982721af"},
	} {
		if got := wpaPSK(v.pass, v.ssid); got != v.psk {
			t.Errorf("%s/%s: %s", v.pass, v.ssid, got)
		}
	}
	// Quotes, backslashes and exactly 32 characters give a key, never a
	// quoted or hex-encoded passphrase.
	for _, p := range []string{`pa"ss\word`, strings.Repeat("x", 32)} {
		c := wpaConf(WiFi{Name: "wlan0", SSID: "Home", PSK: p, Enabled: true})
		if !strings.Contains(c, "psk="+wpaPSK(p, "Home")+"\n") {
			t.Errorf("%q:\n%s", p, c)
		}
	}
}

func TestActivateWiFiRestartsOrStops(t *testing.T) {
	dir := t.TempDir()
	old, oldRun := WPAConfDir, sysRun
	WPAConfDir = dir
	var calls []string
	sysRun = func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}
	t.Cleanup(func() { WPAConfDir, sysRun = old, oldRun })
	os.WriteFile(filepath.Join(dir, wpaPrefix+"wlan0.conf"), []byte(wpaMarker), 0o600)
	activateWiFi([]string{"wlan0", "wlan1", "wlan0"})
	want := []string{"systemctl enable wpa_supplicant@wlan0.service", "systemctl restart wpa_supplicant@wlan0.service", "systemctl disable --now wpa_supplicant@wlan1.service"}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls: %q", calls)
	}
}

func TestScanParse(t *testing.T) {
	scan := `BSS aa:bb:cc:dd:ee:01(on wlan0)
	TSF: 123
	freq: 2437
	capability: ESS Privacy ShortSlotTime (0x0411)
	signal: -40.00 dBm
	SSID: HomeNet
	RSN:	 * Version: 1
BSS aa:bb:cc:dd:ee:02(on wlan0)
	freq: 5180
	capability: ESS Privacy (0x0411)
	signal: -70.00 dBm
	SSID: HomeNet
	WPA:	 * Version: 1
BSS aa:bb:cc:dd:ee:03(on wlan0)
	freq: 2412
	capability: ESS (0x0001)
	signal: -80.00 dBm
	SSID: Cafe
`
	old := iwRun
	iwRun = func(ctx context.Context, args ...string) (string, error) { return scan, nil }
	t.Cleanup(func() { iwRun = old })
	res, err := Scan(context.Background(), "wlan0")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("dedupe by ssid: %+v", res)
	}
	if res[0].SSID != "HomeNet" || res[0].Signal != -40 || res[0].Security != "WPA2" {
		t.Errorf("strongest first: %+v", res[0])
	}
	if res[1].SSID != "Cafe" || res[1].Security != "open" {
		t.Errorf("open: %+v", res[1])
	}
}

func TestLinkStatus(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "wlan0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wlan0", "operstate"), []byte("up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := SysNetDir
	SysNetDir = dir
	t.Cleanup(func() { SysNetDir = old })
	link := `Connected to aa:bb:cc:dd:ee:01 (on wlan0)
	SSID: HomeNet
	freq: 2437
	signal: -52.00 dBm
`
	oldRun := iwRun
	iwRun = func(ctx context.Context, args ...string) (string, error) { return link, nil }
	t.Cleanup(func() { iwRun = oldRun })
	st := LinkStatus(context.Background(), "wlan0")
	if !st.Connected || st.SSID != "HomeNet" || st.Signal != -52 || st.Freq != 2437 || st.State != "up" {
		t.Fatalf("status: %+v", st)
	}
	iwRun = func(ctx context.Context, args ...string) (string, error) { return "Not connected.\n", nil }
	st = LinkStatus(context.Background(), "wlan0")
	if st.Connected || st.SSID != "" {
		t.Fatalf("not connected: %+v", st)
	}
}

func TestLinksIncludeWiFi(t *testing.T) {
	c := Config{
		Interfaces: []Iface{{Name: "eth0", IP: IP{Mode: "dhcp"}}},
		WiFi:       []WiFi{{Name: "wlan0", SSID: "x", Enabled: true}},
	}
	got := links(c)
	found := false
	for _, l := range got {
		if l == "wlan0" {
			found = true
		}
	}
	if !found {
		t.Errorf("links() = %v, want wlan0", got)
	}
}
