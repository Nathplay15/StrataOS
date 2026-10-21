package cast

import (
	"context"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Receiver is a device that can play a URL.
type Receiver struct {
	ID    string `json:"id"`   // "cc:<device id>" or "dlna:<UDN>"
	Kind  string `json:"kind"` // chromecast | dlna
	Name  string `json:"name"`
	Model string `json:"model,omitempty"`
	Addr  string `json:"addr"` // host:port (Chromecast) or IP (DLNA)
	// DLNA renderers: the control URLs of their services.
	AVTransport      string `json:"-"`
	RenderingControl string `json:"-"`
	Video            bool   `json:"video"` // can show videos and pictures (audio-only speakers cannot)
}

const castService = "_googlecast._tcp.local."

// mdnsQuery builds a PTR question for a service; sent from an ephemeral port it
// is a "legacy unicast" query (RFC 6762 §6.7) answered straight to the sender,
// so the mDNS port held by avahi-daemon is not needed.
func mdnsQuery(service string) ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(service), Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	return b.Finish()
}

type castInstance struct {
	name, target, id, fn, model string
	port                        int
	ip                          netip.Addr
	audioOnly                   bool
}

// parseCastAnswer reads the records of one mDNS answer into the instances
// map (by instance name) and the addresses of host names.
func parseCastAnswer(msg []byte, from netip.Addr, inst map[string]*castInstance, hosts map[string]netip.Addr) {
	var p dnsmessage.Parser
	if _, err := p.Start(msg); err != nil {
		return
	}
	if err := p.SkipAllQuestions(); err != nil {
		return
	}
	get := func(n string) *castInstance {
		n = strings.ToLower(n)
		if inst[n] == nil {
			inst[n] = &castInstance{name: n, ip: from}
		}
		return inst[n]
	}
	read := func(next func() (dnsmessage.Resource, error)) {
		for {
			r, err := next()
			if err != nil {
				return
			}
			name := r.Header.Name.String()
			lname := strings.ToLower(name)
			switch b := r.Body.(type) {
			case *dnsmessage.PTRResource:
				if lname == castService {
					get(b.PTR.String())
				}
			case *dnsmessage.SRVResource:
				if strings.HasSuffix(lname, "."+castService) {
					i := get(name)
					i.target, i.port = strings.ToLower(b.Target.String()), int(b.Port)
				}
			case *dnsmessage.TXTResource:
				if strings.HasSuffix(lname, "."+castService) {
					i := get(name)
					for _, kv := range b.TXT {
						k, v, _ := strings.Cut(kv, "=")
						switch k {
						case "id":
							i.id = v
						case "fn":
							i.fn = v
						case "md":
							i.model = v
						case "ca":
							// Capability bit 0 is video output: Google/Nest speakers lack it.
							if n, err := strconv.Atoi(v); err == nil {
								i.audioOnly = n&1 == 0
							}
						}
					}
				}
			case *dnsmessage.AResource:
				hosts[lname] = netip.AddrFrom4(b.A)
			}
		}
	}
	read(p.Answer)
	if p.SkipAllAuthorities() != nil {
		return
	}
	read(p.Additional)
}

// castReceivers turns the gathered records into receivers.
func castReceivers(inst map[string]*castInstance, hosts map[string]netip.Addr) []Receiver {
	var out []Receiver
	for _, i := range inst {
		if i.port == 0 {
			i.port = 8009
		}
		ip := i.ip
		if a, ok := hosts[i.target]; ok {
			ip = a
		}
		if !ip.IsValid() {
			continue
		}
		name := i.fn
		if name == "" {
			name = strings.TrimSuffix(strings.TrimSuffix(i.name, "."+castService), ".")
		}
		id := i.id
		if id == "" {
			id = i.name
		}
		out = append(out, Receiver{ID: "cc:" + id, Kind: "chromecast", Name: name, Model: i.model, Addr: net.JoinHostPort(ip.String(), strconv.Itoa(i.port)), Video: !i.audioOnly})
	}
	return out
}

// DiscoverChromecasts queries the local network for Cast devices.
func DiscoverChromecasts(ctx context.Context, wait time.Duration) []Receiver {
	q, err := mdnsQuery(castService)
	if err != nil {
		return nil
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil
	}
	defer conn.Close()
	dst := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	inst, hosts := map[string]*castInstance{}, map[string]netip.Addr{}
	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	// Asked twice: multicast datagrams get lost.
	_, _ = conn.WriteToUDP(q, dst)
	go func() {
		time.Sleep(wait / 3)
		_, _ = conn.WriteToUDP(q, dst)
	}()
	buf := make([]byte, 9000)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			break
		}
		parseCastAnswer(buf[:n], from.Addr().Unmap(), inst, hosts)
	}
	return castReceivers(inst, hosts)
}

// Discover finds Chromecast devices and DLNA renderers in parallel.
func Discover(ctx context.Context, wait time.Duration) []Receiver {
	var mu sync.Mutex
	var wg sync.WaitGroup
	out := []Receiver{}
	add := func(rs []Receiver) {
		mu.Lock()
		out = append(out, rs...)
		mu.Unlock()
	}
	wg.Add(2)
	go func() { defer wg.Done(); add(DiscoverChromecasts(ctx, wait)) }()
	go func() { defer wg.Done(); add(DiscoverRenderers(ctx, wait)) }()
	wg.Wait()
	seen := map[string]bool{}
	uniq := out[:0]
	for _, r := range out {
		if !seen[r.ID] {
			seen[r.ID] = true
			uniq = append(uniq, r)
		}
	}
	sort.Slice(uniq, func(i, j int) bool { return strings.ToLower(uniq[i].Name) < strings.ToLower(uniq[j].Name) })
	return uniq
}
