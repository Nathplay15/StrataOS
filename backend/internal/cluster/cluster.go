// Package cluster is the group of StrataOS NAS that work together (Ceph
// storage now, VM clustering later): who the members are, where they are
// reached, and the authenticated calls between them. Trust follows the high
// availability pairing: a one-time code shown on the joining NAS, then a
// shared token and the certificates pinned at join time.
package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"strataos/internal/store"
	"strataos/internal/sys"
)

// Header carries the cluster token on peer calls.
const Header = "X-Strata-Cluster"

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,39}$`)

// ValidName checks a cluster name.
func ValidName(n string) error {
	if !nameRe.MatchString(n) {
		return fmt.Errorf("invalid cluster name (letters, digits, spaces, . _ -; 40 characters at most)")
	}
	return nil
}

// daemonRe is what Ceph accepts as a daemon name (mon.<name>…): the short
// hostname of each member.
var daemonRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// ValidDaemonName checks a hostname used as the Ceph name of a member.
func ValidDaemonName(n string) error {
	if !daemonRe.MatchString(n) {
		return fmt.Errorf("host name %q cannot name Ceph daemons: use lowercase letters, digits and dashes", n)
	}
	return nil
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)

// ValidHost checks an address typed to reach another NAS: an IP address or
// a DNS name, without port, scheme or path.
func ValidHost(h string) error {
	if net.ParseIP(h) != nil {
		return nil
	}
	if !hostRe.MatchString(h) || strings.Contains(h, "..") {
		return fmt.Errorf("invalid address %q", h)
	}
	return nil
}

// ValidNet checks an IPv4 or IPv6 network in CIDR notation; "" is allowed
// when optional.
func ValidNet(cidr string, optional bool) error {
	if cidr == "" && optional {
		return nil
	}
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil || !ip.Equal(n.IP) {
		return fmt.Errorf("invalid network %q (e.g. 192.168.10.0/24)", cidr)
	}
	return nil
}

// AddrIn returns the first address of addrs (CIDR notation, as `ip -o addr`
// prints them) inside network cidr.
func AddrIn(addrs []string, cidr string) string {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ip, _, err := net.ParseCIDR(a)
		if err == nil && n.Contains(ip) {
			return ip.String()
		}
	}
	return ""
}

// ParseAddrs reads the output of `ip -o addr show` into interface → addresses
// (CIDR), leaving out loopback and container or VM bridges.
func ParseAddrs(out string) map[string][]string {
	m := map[string][]string{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 4 || (f[2] != "inet" && f[2] != "inet6") {
			continue
		}
		ifc := strings.TrimSuffix(f[1], ":")
		if ifc == "lo" || strings.HasPrefix(ifc, "docker") || strings.HasPrefix(ifc, "br-") || strings.HasPrefix(ifc, "virbr") || strings.HasPrefix(ifc, "veth") {
			continue
		}
		if f[2] == "inet6" && strings.HasPrefix(f[3], "fe80:") {
			continue
		}
		m[ifc] = append(m[ifc], f[3])
	}
	return m
}

// LocalAddrs lists the addresses of this NAS (CIDR notation).
func LocalAddrs() []string {
	out, _ := sys.Run("ip", "-o", "addr", "show")
	var all []string
	m := ParseAddrs(out)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		all = append(all, m[k]...)
	}
	return all
}

// Networks proposes the networks this NAS has an address in (for the wizard).
func Networks(addrs []string) []string {
	var out []string
	for _, a := range addrs {
		if _, n, err := net.ParseCIDR(a); err == nil && !slices.Contains(out, n.String()) {
			out = append(out, n.String())
		}
	}
	return out
}

// Member returns the member with this ID.
func Member(c store.Cluster, id string) (store.ClusterMember, bool) {
	for _, m := range c.Members {
		if m.ID == id {
			return m, true
		}
	}
	return store.ClusterMember{}, false
}

// Others lists the members except this NAS.
func Others(c store.Cluster) []store.ClusterMember {
	var out []store.ClusterMember
	for _, m := range c.Members {
		if m.ID != c.Self {
			out = append(out, m)
		}
	}
	return out
}

// TokenOK compares a presented token with the cluster's in constant time.
func TokenOK(c store.Cluster, presented string) bool {
	return c.ID != "" && len(c.Token) == 64 && subtle.ConstantTimeCompare([]byte(presented), []byte(c.Token)) == 1
}

// Peer calls another member's cluster API over HTTPS, pinning its certificate.
type Peer struct {
	Host, Token, Cert string
}

// PeerOf is the client for member m.
func PeerOf(c store.Cluster, m store.ClusterMember) Peer {
	return Peer{Host: m.Host, Token: c.Token, Cert: m.Cert}
}

func (p Peer) client() *http.Client {
	fp := p.Cert
	cfg := &tls.Config{InsecureSkipVerify: true, VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return fmt.Errorf("no certificate")
		}
		sum := sha256.Sum256(raw[0])
		if fp == "" || subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(fp)) != 1 {
			return fmt.Errorf("%s presented an unexpected certificate (remove it from the cluster and add it again if it was replaced)", p.Host)
		}
		return nil
	}}
	// No overall timeout: calls are bounded by their context (some, such as
	// creating an OSD, take minutes).
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}
}

// Client is the HTTP client that pins the member's certificate (streams,
// websockets).
func (p Peer) Client() *http.Client { return p.client() }

// URL is the address of a peer API path.
func (p Peer) URL(path string) string {
	return "https://" + net.JoinHostPort(p.Host, "7443") + "/api/cluster/peer/" + path
}

// PeerError is an error answer of the other NAS.
type PeerError struct {
	Status int
	Msg    string
}

func (e *PeerError) Error() string { return e.Msg }

// Call sends a JSON request to https://host:7443/api/cluster/peer/<path>.
func (p Peer) Call(ctx context.Context, path string, in, out any) error {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+net.JoinHostPort(p.Host, "7443")+"/api/cluster/peer/"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Strata", "1")
	if p.Token != "" {
		req.Header.Set(Header, p.Token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", p.Host, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &PeerError{Status: resp.StatusCode, Msg: p.Host + ": " + msg}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
