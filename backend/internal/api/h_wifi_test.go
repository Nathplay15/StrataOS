package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos/internal/network"
	"strataos/internal/store"
)

// wifiServer builds a test server with a fake wireless link (wlan0) and a saved
// WiFi configuration holding a password.
func wifiServer(t *testing.T) (*Server, string) {
	t.Helper()
	sysNet := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysNet, "wlan0", "wireless"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sysNet, "eth0"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := network.SysNetDir
	network.SysNetDir = sysNet
	t.Cleanup(func() { network.SysNetDir = old })
	s := newTestServer(t)
	s.systemRoutes()
	s.wifiRoutes()
	_ = s.store.Update(func(st *store.State) error {
		st.Users = append(st.Users, &store.User{Username: "admin", Role: "admin"})
		st.Network = &network.Config{
			Interfaces: []network.Iface{{Name: "wlan0", IP: network.IP{Mode: "dhcp"}}},
			WiFi:       []network.WiFi{{Name: "wlan0", SSID: "HomeNet", PSK: "supersecret1", Enabled: true}},
		}
		return nil
	})
	tok := s.sessions.create("admin", "admin", "127.0.0.1", time.Hour)
	return s, tok
}

func wifiDo(t *testing.T, s *Server, tok, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
	req.Header.Set("X-Strata", "1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec
}

func TestWiFiStatusRoute(t *testing.T) {
	s, tok := wifiServer(t)
	rec := wifiDo(t, s, tok, "GET", "/api/system/network/wifi", "")
	if rec.Code != 200 {
		t.Fatalf("wifi status: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"name":"wlan0"`) || !strings.Contains(body, `"pskSet":true`) || !strings.Contains(body, `"ssid":"HomeNet"`) {
		t.Errorf("wifi status body: %s", body)
	}
	if strings.Contains(body, "supersecret1") {
		t.Errorf("the WiFi password must never be sent: %s", body)
	}
}

func TestNetworkConfigMasksWiFiPSK(t *testing.T) {
	s, tok := wifiServer(t)
	rec := wifiDo(t, s, tok, "GET", "/api/system/network", "")
	if rec.Code != 200 {
		t.Fatalf("network: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"psk":"********"`) {
		t.Errorf("psk must be masked: %s", body)
	}
	if strings.Contains(body, "supersecret1") {
		t.Errorf("the WiFi password must never be sent: %s", body)
	}
}

func TestWiFiScanRejectsNonWireless(t *testing.T) {
	s, tok := wifiServer(t)
	rec := wifiDo(t, s, tok, "POST", "/api/system/network/wifi/scan", `{"iface":"eth0"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "not a wireless interface") {
		t.Fatalf("scan on eth0: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRestoreMaskedWiFiPSK(t *testing.T) {
	prev := network.Config{WiFi: []network.WiFi{{Name: "wlan0", SSID: "HomeNet", PSK: "supersecret1", Enabled: true}}}
	req := &network.Config{WiFi: []network.WiFi{
		{Name: "wlan0", SSID: "HomeNet", PSK: "********", Enabled: true},
		{Name: "wlan0x", SSID: "New", PSK: "brandnewpass", Enabled: true},
	}}
	restoreMaskedWiFiPSK(req, prev)
	if req.WiFi[0].PSK != "supersecret1" {
		t.Errorf("masked psk not restored: %q", req.WiFi[0].PSK)
	}
	if req.WiFi[1].PSK != "brandnewpass" {
		t.Errorf("new psk must be kept: %q", req.WiFi[1].PSK)
	}
}
