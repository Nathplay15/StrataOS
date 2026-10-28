// Package mdns finds the StrataOS NAS of the local network: a one-shot
// multicast DNS query for the _strataos._tcp service the NAS announces
// (packaging/avahi/strata.service), with its HTTPS port in the TXT record.
package mdns

import (
	"context"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// NAS is a NAS found on the network.
type NAS struct {
	Name string `json:"name"`
	Host string `json:"host"`
	Port int    `json:"port"`
	URL  string `json:"url"`
}

const service = "_strataos._tcp.local."

// Discover asks the network for a while and returns what answered.
func Discover(ctx context.Context, wait time.Duration) ([]NAS, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	q, err := query()
	if err != nil {
		return nil, err
	}
	dst := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	if _, err := conn.WriteToUDP(q, dst); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	found := map[string]*NAS{}
	instHost := map[string]string{}
	hostIP := map[string]string{}
	buf := make([]byte, 9000)
	resent := false
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if !resent && time.Now().Before(deadline) {
				continue
			}
			break
		}
		if !resent && time.Until(deadline) < wait/2 {
			_, _ = conn.WriteToUDP(q, dst) // a second chance for slow responders
			resent = true
		}
		parse(buf[:n], from.IP.String(), found, instHost, hostIP)
	}
	var out []NAS
	for inst, f := range found {
		if h := instHost[inst]; h != "" {
			if ip := hostIP[h]; ip != "" {
				f.Host = ip
			}
		}
		if f.Port == 0 {
			f.Port = 7443
		}
		f.URL = "https://" + net.JoinHostPort(f.Host, strconv.Itoa(f.Port))
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func query() ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(service), Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	return b.Finish()
}

func parse(msg []byte, from string, found map[string]*NAS, instHost, hostIP map[string]string) {
	var p dnsmessage.Parser
	if _, err := p.Start(msg); err != nil {
		return
	}
	_ = p.SkipAllQuestions()
	var rrs []dnsmessage.Resource
	for _, section := range []func() ([]dnsmessage.Resource, error){p.AllAnswers, p.AllAuthorities, p.AllAdditionals} {
		r, err := section()
		if err != nil {
			break
		}
		rrs = append(rrs, r...)
	}
	get := func(inst string) *NAS {
		f := found[inst]
		if f == nil {
			name := strings.TrimSuffix(inst, "."+service)
			name = strings.TrimPrefix(strings.ReplaceAll(name, `\ `, " "), "StrataOS on ")
			f = &NAS{Name: name, Host: from}
			found[inst] = f
		}
		return f
	}
	for _, rr := range rrs {
		switch b := rr.Body.(type) {
		case *dnsmessage.PTRResource:
			if strings.EqualFold(rr.Header.Name.String(), service) {
				get(b.PTR.String())
			}
		case *dnsmessage.SRVResource:
			if strings.HasSuffix(strings.ToLower(rr.Header.Name.String()), service) {
				f := get(rr.Header.Name.String())
				if f.Port == 0 {
					f.Port = int(b.Port)
				}
				instHost[rr.Header.Name.String()] = b.Target.String()
			}
		case *dnsmessage.TXTResource:
			if strings.HasSuffix(strings.ToLower(rr.Header.Name.String()), service) {
				f := get(rr.Header.Name.String())
				for _, t := range b.TXT {
					if v, ok := strings.CutPrefix(t, "https="); ok {
						if n, err := strconv.Atoi(v); err == nil {
							f.Port = n
						}
					}
				}
			}
		case *dnsmessage.AResource:
			hostIP[rr.Header.Name.String()] = net.IP(b.A[:]).String()
		}
	}
}
