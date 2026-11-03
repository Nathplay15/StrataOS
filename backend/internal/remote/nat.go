package remote

// The router's own external address, asked over NAT-PMP or UPnP IGD: compared
// with the address the Internet sees, it tells whether the provider puts the
// line behind carrier-grade NAT, where port forwarding cannot work. Both are
// local protocols: nothing leaves the network.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ParseNATPMP reads the answer to a NAT-PMP external address request (RFC 6886).
func ParseNATPMP(b []byte) (netip.Addr, error) {
	if len(b) < 12 || b[0] != 0 || b[1] != 128 {
		return netip.Addr{}, errors.New("not a NAT-PMP answer")
	}
	if code := binary.BigEndian.Uint16(b[2:4]); code != 0 {
		return netip.Addr{}, fmt.Errorf("NAT-PMP error %d", code)
	}
	return netip.AddrFrom4([4]byte(b[8:12])), nil
}

// natPMP asks the gateway on UDP 5351.
func natPMP(ctx context.Context, gw netip.Addr) (netip.Addr, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp4", netip.AddrPortFrom(gw, 5351).String())
	if err != nil {
		return netip.Addr{}, err
	}
	defer c.Close()
	buf := make([]byte, 16)
	for wait := 250 * time.Millisecond; wait <= time.Second; wait *= 2 {
		if _, err := c.Write([]byte{0, 0}); err != nil {
			return netip.Addr{}, err
		}
		_ = c.SetReadDeadline(time.Now().Add(wait))
		n, err := c.Read(buf)
		if err == nil {
			return ParseNATPMP(buf[:n])
		}
		if ctx.Err() != nil {
			return netip.Addr{}, ctx.Err()
		}
	}
	return netip.Addr{}, errors.New("no NAT-PMP answer")
}

// SSDPLocation returns the LOCATION header of an SSDP answer.
func SSDPLocation(b []byte) string {
	r := bufio.NewReader(bytes.NewReader(b))
	line, _ := r.ReadString('\n')
	if !strings.HasPrefix(line, "HTTP/1.1 200") {
		return ""
	}
	for {
		line, err := r.ReadString('\n')
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "location") {
			return strings.TrimSpace(v)
		}
		if err != nil {
			return ""
		}
	}
}

var wanService = regexp.MustCompile(`^urn:schemas-upnp-org:service:WAN(IP|PPP)Connection:[0-9]$`)

type igdDevice struct {
	Services []struct {
		Type       string `xml:"serviceType"`
		ControlURL string `xml:"controlURL"`
	} `xml:"serviceList>service"`
	Devices []igdDevice `xml:"deviceList>device"`
}

// ParseIGD finds the WAN connection service of a router description and
// returns its control URL (resolved against base) and service type.
func ParseIGD(desc []byte, base *url.URL) (*url.URL, string, error) {
	var root struct {
		URLBase string    `xml:"URLBase"`
		Device  igdDevice `xml:"device"`
	}
	if err := xml.Unmarshal(desc, &root); err != nil {
		return nil, "", err
	}
	var walk func(d igdDevice) (string, string)
	walk = func(d igdDevice) (string, string) {
		for _, s := range d.Services {
			if wanService.MatchString(strings.TrimSpace(s.Type)) {
				return strings.TrimSpace(s.ControlURL), strings.TrimSpace(s.Type)
			}
		}
		for _, c := range d.Devices {
			if u, t := walk(c); u != "" {
				return u, t
			}
		}
		return "", ""
	}
	ctl, typ := walk(root.Device)
	if ctl == "" {
		return nil, "", errors.New("no WAN connection service")
	}
	if root.URLBase != "" {
		if b, err := url.Parse(root.URLBase); err == nil {
			base = b
		}
	}
	u, err := base.Parse(ctl)
	if err != nil {
		return nil, "", err
	}
	return u, typ, nil
}

// ParseExternalIP reads the answer of GetExternalIPAddress.
func ParseExternalIP(b []byte) (netip.Addr, error) {
	i := bytes.Index(b, []byte("NewExternalIPAddress>"))
	if i < 0 {
		return netip.Addr{}, errors.New("no external address in the answer")
	}
	rest := b[i+len("NewExternalIPAddress>"):]
	j := bytes.IndexByte(rest, '<')
	if j < 0 {
		return netip.Addr{}, errors.New("malformed answer")
	}
	return netip.ParseAddr(strings.TrimSpace(string(rest[:j])))
}

// upnp searches the router with SSDP and asks its WAN connection service.
// Only answers from the gateway itself are followed, and only on its address.
func upnp(ctx context.Context, gw netip.Addr) (netip.Addr, error) {
	c, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return netip.Addr{}, err
	}
	defer c.Close()
	req := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 1\r\nST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n\r\n"
	dst := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	_, _ = c.WriteToUDP([]byte(req), dst)
	_, _ = c.WriteToUDP([]byte(req), dst)
	deadline := time.Now().Add(2 * time.Second)
	_ = c.SetReadDeadline(deadline)
	buf := make([]byte, 2048)
	var loc *url.URL
	for loc == nil {
		n, from, err := c.ReadFromUDPAddrPort(buf)
		if err != nil {
			return netip.Addr{}, errors.New("no UPnP router answered")
		}
		if from.Addr().Unmap() != gw {
			continue
		}
		u, err := url.Parse(SSDPLocation(buf[:n]))
		if err != nil || u.Scheme != "http" || u.Hostname() != gw.String() {
			continue
		}
		loc = u
	}
	cl := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, loc.String(), nil)
	resp, err := cl.Do(hreq)
	if err != nil {
		return netip.Addr{}, err
	}
	desc, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	resp.Body.Close()
	ctl, typ, err := ParseIGD(desc, loc)
	if err != nil {
		return netip.Addr{}, err
	}
	if ctl.Scheme != "http" || ctl.Hostname() != gw.String() {
		return netip.Addr{}, errors.New("the router points to another host")
	}
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:GetExternalIPAddress xmlns:u="` + typ + `"/></s:Body></s:Envelope>`
	hreq, _ = http.NewRequestWithContext(ctx, http.MethodPost, ctl.String(), strings.NewReader(body))
	hreq.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	hreq.Header.Set("SOAPAction", `"`+typ+`#GetExternalIPAddress"`)
	resp, err = cl.Do(hreq)
	if err != nil {
		return netip.Addr{}, err
	}
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	return ParseExternalIP(out)
}

// RouterExternalIP asks the gateway for its external address, NAT-PMP first
// (one packet), then UPnP; method says which one answered.
func RouterExternalIP(ctx context.Context, gw netip.Addr) (addr netip.Addr, method string, err error) {
	if !gw.Is4() {
		return netip.Addr{}, "", errors.New("no IPv4 gateway")
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if a, err := natPMP(ctx, gw); err == nil && a.IsValid() && !a.IsUnspecified() {
		return a, "nat-pmp", nil
	}
	a, err := upnp(ctx, gw)
	if err != nil {
		return netip.Addr{}, "", err
	}
	return a, "upnp", nil
}
