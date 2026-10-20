package remote

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"strataos/internal/store"
)

const tsRunning = `{
  "Version": "1.90.0",
  "BackendState": "Running",
  "AuthURL": "",
  "Self": {"HostName": "strata-vm", "DNSName": "strata-vm.tail1234.ts.net.", "OS": "linux", "TailscaleIPs": ["100.101.1.2", "fd7a:115c:a1e0::1"], "Online": true},
  "Health": [],
  "MagicDNSSuffix": "tail1234.ts.net",
  "CurrentTailnet": {"Name": "marie@example.com", "MagicDNSSuffix": "tail1234.ts.net", "MagicDNSEnabled": true},
  "Peer": {
    "nodekey:a": {"HostName": "phone", "DNSName": "phone.tail1234.ts.net.", "OS": "android", "TailscaleIPs": ["100.101.1.3"], "Online": true},
    "nodekey:b": {"HostName": "laptop", "DNSName": "laptop.tail1234.ts.net.", "OS": "windows", "TailscaleIPs": ["100.101.1.4"], "Online": false},
    "nodekey:c": {"HostName": "desktop", "DNSName": "desktop.tail1234.ts.net.", "OS": "linux", "TailscaleIPs": ["100.101.1.5"], "Online": true, "ExitNode": true}
  }
}`

func TestParseTSStatus(t *testing.T) {
	st, err := ParseTSStatus([]byte(tsRunning))
	if err != nil {
		t.Fatal(err)
	}
	if !st.LoggedIn || st.Name != "strata-vm" || st.DNSName != "strata-vm.tail1234.ts.net" || len(st.IPs) != 2 || !st.MagicDNS || st.Tailnet != "marie@example.com" {
		t.Errorf("self: %+v", st)
	}
	if st.Online != 2 || len(st.Peers) != 3 || st.Peers[0].Name != "desktop" || st.Peers[1].Name != "phone" || st.Peers[2].Online || !st.Peers[0].ExitNode {
		t.Errorf("peers: %+v", st.Peers)
	}
	st, err = ParseTSStatus([]byte(`{"BackendState":"NeedsLogin","AuthURL":"https://login.tailscale.com/a/abc","Self":{"HostName":"strata-vm","TailscaleIPs":null},"Peer":null}`))
	if err != nil || st.LoggedIn || st.AuthURL != "https://login.tailscale.com/a/abc" || st.Name != "" || st.Peers == nil {
		t.Errorf("logged out: %+v %v", st, err)
	}
	if _, err := ParseTSStatus([]byte("failed to connect to local tailscaled")); err == nil {
		t.Error("garbage parsed")
	}
}

func TestTailscaleOptions(t *testing.T) {
	c := store.Tailscale{AdvertiseLAN: true, Routes: "192.168.1.7/24, 10.0.0.0/8\n192.168.1.0/24"}
	if err := ValidateTailscale(&c); err != nil || c.Routes != "10.0.0.0/8,192.168.1.0/24" {
		t.Errorf("routes %q %v", c.Routes, err)
	}
	for _, bad := range []string{"0.0.0.0/0", "192.168.1.0", "10.0.0.0/8;id", "--ssh"} {
		if err := ValidateTailscale(&store.Tailscale{Routes: bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	got := TailscaleSetArgs(c, "192.168.50.0/24")
	want := []string{"set", "--advertise-routes=10.0.0.0/8,192.168.1.0/24", "--advertise-exit-node=false", "--accept-routes=false", "--ssh=false"}
	if !slices.Equal(got, want) {
		t.Errorf("args %v", got)
	}
	got = TailscaleSetArgs(store.Tailscale{AdvertiseLAN: true, ExitNode: true, SSH: true}, "192.168.50.0/24")
	if got[1] != "--advertise-routes=192.168.50.0/24" || got[2] != "--advertise-exit-node=true" || got[4] != "--ssh=true" {
		t.Errorf("lan guess %v", got)
	}
	if got := TailscaleSetArgs(store.Tailscale{Routes: "10.0.0.0/8"}, "x"); got[1] != "--advertise-routes=" {
		t.Errorf("routes advertised while off: %v", got)
	}
}

func TestParseTunnelToken(t *testing.T) {
	raw := `{"a":"0123456789abcdef0123456789abcdef","t":"6F1B3C2A-1234-4abc-9def-0123456789ab","s":"c2VjcmV0"}`
	tok := base64.StdEncoding.EncodeToString([]byte(raw))
	for _, in := range []string{tok, "  " + tok + "\n", "sudo cloudflared service install " + tok} {
		got, id, err := ParseTunnelToken(in)
		if err != nil || got != tok || id != "6f1b3c2a-1234-4abc-9def-0123456789ab" {
			t.Errorf("%q: %q %q %v", in, got, id, err)
		}
	}
	for _, bad := range []string{"", "notbase64!!", base64.StdEncoding.EncodeToString([]byte(`{"a":"x","t":"not-a-uuid","s":"y"}`)), base64.StdEncoding.EncodeToString([]byte(`{"t":"6f1b3c2a-1234-4abc-9def-0123456789ab"}`)), tok + "\"\nExecStart=/bin/sh"} {
		if _, _, err := ParseTunnelToken(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestTunnelUnit(t *testing.T) {
	u := TunnelUnit("/usr/bin/cloudflared")
	for _, want := range []string{"EnvironmentFile=" + TunnelEnvPath, "ExecStart=/usr/bin/cloudflared --no-autoupdate --metrics 127.0.0.1:20241 tunnel run", "DynamicUser=yes"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q", want)
		}
	}
	if strings.Contains(u, "TUNNEL_TOKEN") {
		t.Error("token in the unit file")
	}
	n, id := ParseReady([]byte(`{"status":200,"readyConnections":4,"connectorId":"0c1f8e2a-aaaa-bbbb-cccc-0123456789ab"}`))
	if n != 4 || id != "0c1f8e2a-aaaa-bbbb-cccc-0123456789ab" {
		t.Errorf("ready %d %q", n, id)
	}
	if n, _ := ParseReady([]byte(`{"status":503,"readyConnections":0}`)); n != 0 {
		t.Error("not ready")
	}
}
