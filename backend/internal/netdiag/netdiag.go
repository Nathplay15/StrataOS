// Package netdiag runs network diagnostics from the NAS itself: name
// resolution, reachability (ping), one TCP port, the default gateway and the
// access to the Internet. The Network page shows the results.
package netdiag

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"strataos/internal/sys"
)

// Check is the result of one diagnostic check.
type Check struct {
	Name   string `json:"name"` // dns | ping | port | gateway | internet
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"` // short summary, English
	// Latency in ms (ping average, TCP connect) and loss in % (ping).
	Latency float64  `json:"latency,omitempty"`
	Loss    float64  `json:"loss,omitempty"`
	IPs     []string `json:"ips,omitempty"` // dns only
}

const (
	pingCount  = 4
	pingWait   = 2 // seconds ping waits for the last answer
	dialWait   = 3 * time.Second
	lookupWait = 5 * time.Second
)

// probeIP is checked over TCP 443 to tell "the NAS has no Internet" from
// "this host is down". IP on purpose: no name resolution involved.
const probeIP = "1.1.1.1"

// Diagnose runs every check against host (a name or an IP), plus the TCP
// port when port > 0.
func Diagnose(ctx context.Context, host string, port int) []Check {
	host = strings.TrimSpace(host)
	checks := []Check{}

	ips, ok := dnsCheck(ctx, host)
	checks = append(checks, ips...)
	if !ok {
		return append(checks, skipped("ping", host), skipped("gateway", host))
	}
	target := host
	if len(ips[len(ips)-1].IPs) > 0 {
		target = ips[len(ips)-1].IPs[0]
	}
	checks = append(checks, pingCheck(ctx, "ping", target))
	if port > 0 {
		checks = append(checks, portCheck(target, port))
	}
	checks = append(checks, gatewayCheck(ctx))
	checks = append(checks, internetCheck())
	return checks
}

func skipped(name, host string) Check {
	return Check{Name: name, Detail: "Skipped: " + host + " does not resolve"}
}

// dnsCheck resolves host; an IP literal needs no resolution.
func dnsCheck(ctx context.Context, host string) ([]Check, bool) {
	if ip := net.ParseIP(host); ip != nil {
		return []Check{{Name: "dns", OK: true, Detail: host + " is an IP address", IPs: []string{host}}}, true
	}
	c, cancel := context.WithTimeout(ctx, lookupWait)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(c, host)
	if err != nil || len(addrs) == 0 {
		return []Check{{Name: "dns", Detail: host + ": " + errText(err)}}, false
	}
	sort.Strings(addrs)
	return []Check{{Name: "dns", OK: true, Detail: fmt.Sprintf("%s resolves to %d address(es)", host, len(addrs)), IPs: addrs}}, true
}

// pingCheck runs ping(8) and parses its output.
func pingCheck(ctx context.Context, name, host string) Check {
	c, cancel := context.WithTimeout(ctx, time.Duration(pingCount)*time.Second+5*time.Second)
	defer cancel()
	out, err := sys.RunCtx(c, "ping", "-c", strconv.Itoa(pingCount), "-W", strconv.Itoa(pingWait), host)
	loss, min, avg, max, ok := parsePing(out)
	if !ok && err != nil {
		return Check{Name: name, Detail: host + ": no answer" + errSuffix(err)}
	}
	return Check{Name: name, OK: ok, Detail: pingDetail(ok, loss, min, avg, max), Latency: avg, Loss: loss}
}

func pingDetail(ok bool, loss, min, avg, max float64) string {
	if !ok {
		return fmt.Sprintf("No answer (%d%% packet loss)", int(loss))
	}
	return fmt.Sprintf("%.1f ms average (%.1f–%.1f ms), %d%% packet loss", avg, min, max, int(loss))
}

// parsePing reads the summary lines of ping: "4 packets transmitted, 4
// received, 0% packet loss" and "rtt min/avg/max/mdev = …". ok is false when
// no answer came back.
func parsePing(out string) (loss, min, avg, max float64, ok bool) {
	for line := range strings.SplitSeq(out, "\n") {
		if f := strings.Fields(line); len(f) >= 2 {
			if len(f) >= 6 && f[1] == "packets" && f[2] == "transmitted," {
				_, _ = fmt.Sscanf(f[5], "%f%%", &loss)
				received, err1 := strconv.Atoi(strings.TrimSuffix(f[3], ","))
				if err1 == nil && received > 0 {
					ok = true
				}
			}
			if len(f) >= 4 && f[0] == "rtt" {
				_, _ = fmt.Sscanf(f[3], "%f/%f/%f", &min, &avg, &max)
			}
		}
	}
	return loss, min, avg, max, ok
}

// portCheck tries one TCP connection.
func portCheck(host string, port int) Check {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	start := time.Now()
	c, err := net.DialTimeout("tcp", addr, dialWait)
	if err != nil {
		return Check{Name: "port", Detail: addr + ": " + errText(err)}
	}
	c.Close()
	return Check{Name: "port", OK: true, Detail: addr + " accepts connections", Latency: float64(time.Since(start).Microseconds()) / 1000}
}

// gatewayCheck pings the default gateway.
func gatewayCheck(ctx context.Context) Check {
	out, err := sys.Run("ip", "route", "show", "default")
	gw := parseGateway(out)
	if gw == "" {
		return Check{Name: "gateway", Detail: "No default gateway" + errSuffix(err)}
	}
	res := pingCheck(ctx, "gateway", gw)
	res.Detail = gw + ": " + res.Detail
	return res
}

// parseGateway reads the gateway of the default route ("default via
// 192.168.1.1 dev eth0 …").
func parseGateway(route string) string {
	for line := range strings.SplitSeq(route, "\n") {
		if f := strings.Fields(line); len(f) >= 3 && f[0] == "default" && f[1] == "via" {
			return f[2]
		}
	}
	return ""
}

// internetCheck tells whether the NAS reaches the Internet at all.
func internetCheck() Check {
	start := time.Now()
	c, err := net.DialTimeout("tcp", probeIP+":443", dialWait)
	if err != nil {
		return Check{Name: "internet", Detail: "No access to the Internet (" + probeIP + ": " + errText(err) + ")"}
	}
	c.Close()
	return Check{Name: "internet", OK: true, Detail: "The Internet is reachable (" + probeIP + ":443)", Latency: float64(time.Since(start).Microseconds()) / 1000}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	m := err.Error()
	if i := strings.Index(m, ": "); i >= 0 && strings.Count(m, ": ") > 1 {
		m = m[strings.LastIndex(m, ": ")+2:]
	}
	return m
}

func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return " (" + errText(err) + ")"
}
