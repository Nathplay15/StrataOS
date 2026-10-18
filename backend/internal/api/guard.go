package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/guard"
	"strataos/internal/store"
)

var (
	guardWake   = make(chan struct{}, 1) // settings changed
	guardEngine = guard.NewEngine()
	guardGeo    struct {
		sync.Mutex
		v4, v6 []string
	}
)

func (s *Server) guardConfig() guard.Config {
	var c guard.Config
	s.store.Read(func(st *store.State) { c = st.Guard.Config })
	c.Defaults()
	return c
}

// guardFail records a failed sign-in from an address.
func (s *Server) guardFail(ipStr, service, user string) {
	a, ok := guard.ParseIP(ipStr)
	if !ok {
		return
	}
	c := s.guardConfig()
	if guardEngine.Fail(c, a, time.Now()) {
		s.guardBlock(a, service, user, 0)
	}
}

// guardBlock refuses an address (d = 0: duration from the settings, longer
// for a repeat offender).
func (s *Server) guardBlock(a netip.Addr, service, user string, d time.Duration) {
	c := s.guardConfig()
	manual := d > 0
	var count int
	_ = s.store.Update(func(st *store.State) error {
		now := time.Now()
		for _, b := range st.Guard.Blocks {
			if b.IP == a.String() {
				count = b.Count
			}
		}
		count++
		if d == 0 {
			d = guard.Duration(c, count)
		}
		st.Guard.Blocks = slices.DeleteFunc(st.Guard.Blocks, func(b guard.Block) bool { return b.IP == a.String() || now.Sub(b.Until) > 30*24*time.Hour })
		st.Guard.Blocks = append(st.Guard.Blocks, guard.Block{IP: a.String(), At: now, Until: now.Add(d), Service: service, User: user, Count: count, Manual: manual})
		if len(st.Guard.Blocks) > 5000 {
			st.Guard.Blocks = st.Guard.Blocks[len(st.Guard.Blocks)-5000:]
		}
		return nil
	})
	if err := guard.AddBlock(a, d); err != nil {
		_ = s.applyGuard() // table missing: load everything
	}
	if !manual {
		s.store.Log("warning", "guard", user, fmt.Sprintf("Address %s blocked for %s: too many failed sign-ins (%s)", a, shortDuration(d), service))
	}
}

func shortDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%d j", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	default:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
}

// applyGuard loads the nftables table from the settings.
func (s *Server) applyGuard() error {
	var g store.Guard
	s.store.Read(func(st *store.State) { g = st.Guard })
	g.Config.Defaults()
	guardGeo.Lock()
	v4, v6 := guardGeo.v4, guardGeo.v6
	guardGeo.Unlock()
	if g.Config.Geo.Enabled && len(v4) == 0 {
		// Never restrict without the lists (everything from outside would be refused).
		g.Config.Geo.Enabled = false
	}
	return guard.Apply(g.Config, g.Blocks, v4, v6)
}

// loadGeo reads (or downloads) the ranges of the allowed countries.
func (s *Server) loadGeo(ctx context.Context, refresh bool) error {
	c := s.guardConfig()
	if !c.Geo.Enabled {
		return nil
	}
	v4, v6, err := guard.FetchCountries(ctx, c.Geo.Countries, refresh)
	_ = s.store.Update(func(st *store.State) error {
		st.Guard.Config.Geo.Error = ""
		if err != nil {
			st.Guard.Config.Geo.Error = err.Error()
		} else {
			st.Guard.Config.Geo.Updated = time.Now()
		}
		return nil
	})
	if err != nil {
		return err
	}
	guardGeo.Lock()
	guardGeo.v4, guardGeo.v6 = v4, v6
	guardGeo.Unlock()
	return nil
}

// inCountries tells whether an address is in the downloaded ranges.
func inCountries(a netip.Addr) bool {
	guardGeo.Lock()
	defer guardGeo.Unlock()
	l := guardGeo.v4
	if a.Is6() {
		l = guardGeo.v6
	}
	for _, r := range l {
		if p, err := netip.ParsePrefix(r); err == nil && p.Contains(a) {
			return true
		}
	}
	return false
}

var (
	sshFailRe = regexp.MustCompile(`Failed (?:password|keyboard-interactive/pam) for (?:invalid user )?(\S+) from (\S+) port`)
	ftpFailRe = regexp.MustCompile(`\[([^\]]*)\] FAIL LOGIN: Client "([^"]+)"`)
)

// follow runs a log command and passes each line, until ctx ends.
func follow(ctx context.Context, name string, args []string, line func(string)) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line(sc.Text())
	}
	_ = cmd.Wait()
}

func sambaAuthFailure(l string) (ip, user string, ok bool) {
	i := strings.Index(l, "{")
	if !strings.Contains(l, "Authentication") || i < 0 {
		return "", "", false
	}
	var j struct {
		Authentication struct {
			Status        string `json:"status"`
			RemoteAddress string `json:"remoteAddress"`
			ClientAccount string `json:"clientAccount"`
		} `json:"Authentication"`
	}
	if json.Unmarshal([]byte(l[i:]), &j) != nil {
		return "", "", false
	}
	a := j.Authentication
	if a.Status == "" || a.Status == "NT_STATUS_OK" || a.RemoteAddress == "" {
		return "", "", false
	}
	return a.RemoteAddress, a.ClientAccount, true
}

// guardLoop applies the table and follows the logs of SSH/SFTP, FTP and SMB.
func (s *Server) guardLoop() {
	time.Sleep(5 * time.Second)
	_ = s.loadGeo(context.Background(), false)
	_ = s.applyGuard()
	go func() {
		for {
			time.Sleep(24 * time.Hour)
			if s.loadGeo(context.Background(), false) == nil {
				_ = s.applyGuard()
			}
		}
	}()
	for {
		if !s.guardConfig().Enabled {
			select {
			case <-guardWake:
			case <-time.After(10 * time.Second):
			}
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		since := fmt.Sprintf("@%d", time.Now().Add(-2*time.Second).Unix())
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			follow(ctx, "journalctl", []string{"-f", "--since", since, "-o", "cat", "-t", "sshd", "-t", "sshd-session"}, func(l string) {
				if m := sshFailRe.FindStringSubmatch(l); m != nil {
					s.guardFail(m[2], "SSH/SFTP", m[1])
				}
			})
		}()
		go func() {
			defer wg.Done()
			follow(ctx, "journalctl", []string{"-f", "--since", since, "-o", "cat", "-t", "vsftpd"}, func(l string) {
				if m := ftpFailRe.FindStringSubmatch(l); m != nil {
					s.guardFail(m[2], "FTP", m[1])
				}
			})
		}()
		go func() {
			defer wg.Done()
			follow(ctx, "tail", []string{"-F", "-n", "0", "/var/log/samba/strata-auth.log"}, func(l string) {
				if ip, user, ok := sambaAuthFailure(l); ok {
					s.guardFail(ip, "SMB", user)
				}
			})
		}()
		for s.guardConfig().Enabled {
			select {
			case <-guardWake:
			case <-time.After(10 * time.Second):
			}
		}
		cancel()
		wg.Wait()
	}
}

func (s *Server) guardRoutes() {
	s.admin("GET /api/guard", func(r *http.Request) (any, error) {
		var g store.Guard
		s.store.Read(func(st *store.State) { g = st.Guard })
		g.Config.Defaults()
		active := []guard.Block{}
		for _, b := range g.Blocks {
			if time.Now().Before(b.Until) {
				active = append(active, b)
			}
		}
		slices.SortFunc(active, func(a, b guard.Block) int { return b.At.Compare(a.At) })
		guardGeo.Lock()
		n4, n6 := len(guardGeo.v4), len(guardGeo.v6)
		guardGeo.Unlock()
		me, _ := guard.ParseIP(clientIP(r))
		return map[string]any{"config": g.Config, "blocks": active, "geoRanges": n4 + n6, "myIp": me.String(), "myIpLocal": guard.Private(me)}, nil
	})

	s.admin("PUT /api/guard", func(r *http.Request) (any, error) {
		var req guard.Config
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Defaults()
		for i, cc := range req.Geo.Countries {
			req.Geo.Countries[i] = strings.ToLower(strings.TrimSpace(cc))
		}
		if err := req.Validate(); err != nil {
			return nil, badRequest(err.Error())
		}
		prev := s.guardConfig()
		req.Geo.Updated, req.Geo.Error = prev.Geo.Updated, prev.Geo.Error
		if err := s.store.Update(func(st *store.State) error { st.Guard.Config = req; return nil }); err != nil {
			return nil, err
		}
		if req.Enabled && req.Geo.Enabled {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
			defer cancel()
			if err := s.loadGeo(ctx, !slices.Equal(prev.Geo.Countries, req.Geo.Countries)); err != nil {
				_ = s.store.Update(func(st *store.State) error { st.Guard.Config.Geo.Enabled = false; return nil })
				_ = s.applyGuard()
				return nil, badRequest(err.Error())
			}
			// Never lock out the administrator who is making the change.
			if me, ok := guard.ParseIP(clientIP(r)); ok && !guard.Exempt(req, me) && !guard.Private(me) && !inCountries(me) {
				_ = s.store.Update(func(st *store.State) error { st.Guard.Config.Geo.Enabled = false; return nil })
				_ = s.applyGuard()
				return nil, badRequest(fmt.Sprintf("your address %s is not in the chosen countries: you would be cut off", me))
			}
		}
		select {
		case guardWake <- struct{}{}:
		default:
		}
		s.audit(r, "guard", fmt.Sprintf("Address blocking: enabled=%v after %d failures in %d min, countries=%v", req.Enabled, req.MaxFailures, req.WindowMin, req.Geo.Countries))
		return nil, s.applyGuard()
	})

	s.admin("POST /api/guard/unblock", func(r *http.Request) (any, error) {
		var req struct {
			IP string `json:"ip"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		a, ok := guard.ParseIP(req.IP)
		if !ok {
			return nil, badRequest("invalid address")
		}
		_ = s.store.Update(func(st *store.State) error {
			for i := range st.Guard.Blocks {
				if st.Guard.Blocks[i].IP == a.String() {
					st.Guard.Blocks[i].Until = time.Now()
				}
			}
			return nil
		})
		guard.RemoveBlock(a)
		guardEngine.Forget(a)
		s.audit(r, "guard", "Unblocked "+a.String())
		return nil, nil
	})

	s.admin("POST /api/guard/block", func(r *http.Request) (any, error) {
		var req struct {
			IP      string `json:"ip"`
			Minutes int    `json:"minutes"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		a, ok := guard.ParseIP(req.IP)
		if !ok || req.Minutes < 1 || req.Minutes > 525600 {
			return nil, badRequest("invalid address or duration")
		}
		if me, ok := guard.ParseIP(clientIP(r)); ok && me == a {
			return nil, badRequest("you cannot block your own address")
		}
		if !s.guardConfig().Enabled {
			return nil, badRequest("turn the address blocking on first")
		}
		s.guardBlock(a, "manual", "", time.Duration(req.Minutes)*time.Minute)
		s.audit(r, "guard", "Blocked "+a.String())
		return nil, nil
	})
}
