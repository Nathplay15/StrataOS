package netdiag

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

const pingOK = `PING nas.example.com (192.0.2.10) 56(84) bytes of data.
64 bytes from 192.0.2.10: icmp_seq=1 ttl=64 time=0.045 ms
64 bytes from 192.0.2.10: icmp_seq=2 ttl=64 time=0.052 ms
64 bytes from 192.0.2.10: icmp_seq=3 ttl=64 time=0.061 ms
64 bytes from 192.0.2.10: icmp_seq=4 ttl=64 time=0.050 ms

--- nas.example.com ping statistics ---
4 packets transmitted, 4 received, 0% packet loss, time 3060ms
rtt min/avg/max/mdev = 0.045/0.052/0.061/0.006 ms
`

const pingLoss = `PING nas.example.com (192.0.2.10) 56(84) bytes of data.
64 bytes from 192.0.2.10: icmp_seq=1 ttl=64 time=0.045 ms

--- nas.example.com ping statistics ---
4 packets transmitted, 1 received, 75% packet loss, time 3060ms
rtt min/avg/max/mdev = 0.045/0.045/0.045/0.000 ms
`

const pingDead = `PING 192.0.2.99 (192.0.2.99) 56(84) bytes of data.

--- 192.0.2.99 ping statistics ---
4 packets transmitted, 0 received, 100% packet loss, time 3062ms
`

func TestParsePing(t *testing.T) {
	loss, min, avg, max, ok := parsePing(pingOK)
	if !ok || loss != 0 || min != 0.045 || avg != 0.052 || max != 0.061 {
		t.Fatalf("ok ping: %v %v %v %v %v", ok, loss, min, avg, max)
	}
	loss, _, avg, _, ok = parsePing(pingLoss)
	if !ok || loss != 75 || avg != 0.045 {
		t.Fatalf("lossy ping: %v %v %v", ok, loss, avg)
	}
	loss, _, _, _, ok = parsePing(pingDead)
	if ok || loss != 100 {
		t.Fatalf("dead host: %v %v", ok, loss)
	}
	if _, _, _, _, ok := parsePing(""); ok {
		t.Fatal("empty output accepted")
	}
}

func TestParseGateway(t *testing.T) {
	if gw := parseGateway("default via 192.168.1.1 dev eth0 metric 100\n"); gw != "192.168.1.1" {
		t.Fatalf("gateway: %q", gw)
	}
	if gw := parseGateway("192.168.1.0/24 dev eth0 proto kernel\n"); gw != "" {
		t.Fatalf("no default route: %q", gw)
	}
	if gw := parseGateway(""); gw != "" {
		t.Fatalf("empty: %q", gw)
	}
}

func TestPortCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	n, _ := strconv.Atoi(port)
	if res := portCheck("127.0.0.1", n); !res.OK {
		t.Fatalf("open port: %v", res)
	}
	if res := portCheck("127.0.0.1", 1); res.OK {
		t.Fatalf("closed port: %v", res)
	}
}
