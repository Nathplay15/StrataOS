package vpn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"strataos/internal/store"
)

func TestServerAndClient(t *testing.T) {
	priv, pub, err := GenKey()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := base64.StdEncoding.DecodeString(priv); len(b) != 32 || pub == priv {
		t.Fatal("bad key")
	}
	c := store.VPN{Enabled: true, Endpoint: "nas.example.org", LAN: "192.168.1.0/24", PrivateKey: priv, PublicKey: pub}
	if err := Validate(&c); err != nil {
		t.Fatal(err)
	}
	p1, err := NewPeer(c, "", "Téléphone", "nas")
	if err != nil {
		t.Fatal(err)
	}
	c.Peers = append(c.Peers, p1)
	p2, _ := NewPeer(c, "", "Portable", "lan")
	c.Peers = append(c.Peers, p2)
	if p1.IP != "10.8.0.2" || p2.IP != "10.8.0.3" {
		t.Errorf("addresses %s %s", p1.IP, p2.IP)
	}
	if _, err := NewPeer(c, "", "téléphone", "nas"); err == nil {
		t.Error("duplicate name accepted")
	}
	if _, err := NewPeer(c, "", "x\nPostUp = rm -rf /", "nas"); err == nil {
		t.Error("injection in name accepted")
	}
	srv := RenderServer(c)
	if !strings.Contains(RenderFirewall(c), "masquerade") {
		t.Error("no NAT for the VPN network")
	}
	for _, want := range []string{"Address = 10.8.0.1/24", "ListenPort = 51820", "AllowedIPs = 10.8.0.2/32"} {
		if !strings.Contains(srv, want) {
			t.Errorf("server config lacks %q", want)
		}
	}
	cl := RenderClient(c, p2)
	if !strings.Contains(cl, "Endpoint = nas.example.org:51820") || !strings.Contains(cl, "AllowedIPs = 10.8.0.0/24, 192.168.1.0/24") {
		t.Errorf("client config: %s", cl)
	}
	if cl := RenderClient(c, p1); !strings.Contains(cl, "AllowedIPs = 10.8.0.1/32") {
		t.Errorf("nas-only client: %s", cl)
	}
	bad := store.VPN{Subnet: "10.8.0.0/30"}
	if Validate(&bad) == nil {
		t.Error("tiny subnet accepted")
	}
}

func TestDDNS(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.String())
		switch {
		case r.URL.Path == "/ip":
			io.WriteString(w, "203.0.113.7\n")
		case strings.HasSuffix(r.URL.Path, "/dns_records") && r.Method == "GET":
			io.WriteString(w, `{"success":true,"result":[{"id":"rec1","content":"198.51.100.1"}]}`)
		case strings.Contains(r.URL.Path, "/dns_records/rec1") && r.Method == "PUT":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["content"] != "203.0.113.7" || r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(400)
			}
			io.WriteString(w, `{"success":true,"result":{}}`)
		case r.URL.Path == "/update":
			io.WriteString(w, "ok")
		}
	}))
	defer srv.Close()
	IPLookupURL = srv.URL + "/ip"
	CloudflareAPI = srv.URL
	ip, err := PublicIP(context.Background())
	if err != nil || ip != "203.0.113.7" {
		t.Fatalf("public ip %q %v", ip, err)
	}
	cf := store.DDNS{Provider: "cloudflare", Hostname: "nas.example.org", Token: "tok", ZoneID: strings.Repeat("a", 32)}
	if err := ValidateDDNS(&cf); err != nil {
		t.Fatal(err)
	}
	if err := UpdateDDNS(context.Background(), cf, ip); err != nil {
		t.Fatal(err)
	}
	custom := store.DDNS{Provider: "custom", URL: srv.URL + "/update?ip={ip}"}
	if err := ValidateDDNS(&custom); err != nil {
		t.Fatal(err)
	}
	if err := UpdateDDNS(context.Background(), custom, ip); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(got, "|"), "PUT /zones/"+strings.Repeat("a", 32)+"/dns_records/rec1") || !strings.Contains(strings.Join(got, "|"), "/update?ip=203.0.113.7") {
		t.Errorf("calls: %v", got)
	}
	duck := store.DDNS{Provider: "duckdns", Hostname: "mynas.duckdns.org", Token: "t"}
	if err := ValidateDDNS(&duck); err != nil || duck.Hostname != "mynas" || FullHostname(duck) != "mynas.duckdns.org" {
		t.Errorf("duckdns: %v %s", err, duck.Hostname)
	}
}
