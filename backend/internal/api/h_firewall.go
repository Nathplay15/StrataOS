package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/docker"
	"strataos/internal/firewall"
	"strataos/internal/lxc"
	"strataos/internal/network"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/vms"
)

// Distributed firewall (internal/firewall): the configuration lives in
// State.Firewall; fwLoop keeps the nftables tables in line with it and with
// the guests' interfaces, which change at each start.

type fwTrial struct {
	Token    string    `json:"-"`
	Deadline time.Time `json:"deadline"`
	ReadyAt  time.Time `json:"-"`
	config   firewall.Config
	timer    *time.Timer
}

var fwState struct {
	sync.Mutex
	trial    *fwTrial
	applied  string // script loaded last
	err      string
	bindings map[string]firewall.Binding
	kick     chan struct{}
}

func init() { fwState.kick = make(chan struct{}, 1) }

// fwKick asks fwLoop to look at the guests and apply again now.
func fwKick() {
	select {
	case fwState.kick <- struct{}{}:
	default:
	}
}

func cloneFW(c *firewall.Config) firewall.Config {
	var out firewall.Config
	if c != nil {
		b, _ := json.Marshal(c)
		_ = json.Unmarshal(b, &out)
	}
	if out.Host.Rules == nil {
		out.Host.Rules = []firewall.Rule{}
	}
	return out
}

// fwConfig is the configuration in force: the one on trial, else the saved one.
func (s *Server) fwConfig() firewall.Config {
	fwState.Lock()
	t := fwState.trial
	fwState.Unlock()
	if t != nil {
		return cloneFW(&t.config)
	}
	var c firewall.Config
	s.store.Read(func(st *store.State) { c = cloneFW(st.Firewall) })
	return c
}

// netConfigNow is the network configuration in force (on trial or saved).
func (s *Server) netConfigNow() network.Config {
	if p := network.PendingConfig(); p != nil {
		return *p
	}
	var c network.Config
	s.store.Read(func(st *store.State) {
		if st.Network != nil {
			c = *st.Network
		}
	})
	return c
}

// nasAddrs lists the addresses of the NAS (CIDR), from all its links.
func nasAddrs() []string {
	var out []string
	for _, l := range nasAddrsBy() {
		out = append(out, l...)
	}
	slices.Sort(out)
	return out
}

// nasAddrsBy lists the addresses of the NAS by link.
func nasAddrsBy() map[string][]string {
	addrs := map[string][]string{}
	out, err := sys.Run("ip", "-j", "addr")
	if err != nil {
		return addrs
	}
	var raw []struct {
		Ifname   string `json:"ifname"`
		AddrInfo []struct {
			Local     string `json:"local"`
			Prefixlen int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	_ = json.Unmarshal([]byte(out), &raw)
	for _, r := range raw {
		if r.Ifname == "lo" {
			continue
		}
		for _, a := range r.AddrInfo {
			if a.Local != "" {
				addrs[r.Ifname] = append(addrs[r.Ifname], a.Local+"/"+strconv.Itoa(a.Prefixlen))
			}
		}
	}
	return addrs
}

// hostIfaces maps interface indexes to names.
func hostIfaces() map[int]string {
	m := map[int]string{}
	entries, _ := os.ReadDir("/sys/class/net")
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join("/sys/class/net", e.Name(), "ifindex"))
		if err != nil {
			continue
		}
		if i, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			m[i] = e.Name()
		}
	}
	return m
}

// vethPeers returns the host side of the veth interfaces of a process's
// network namespace (macvlan and other kinds are not bridge ports).
func vethPeers(pid int, idx map[int]string) []string {
	if pid <= 0 {
		return nil
	}
	out, err := sys.Run("nsenter", "-t", strconv.Itoa(pid), "-n", "ip", "-j", "-d", "link", "show")
	if err != nil {
		return nil
	}
	var links []struct {
		LinkIndex int `json:"link_index"`
		LinkInfo  struct {
			Kind string `json:"info_kind"`
		} `json:"linkinfo"`
	}
	_ = json.Unmarshal([]byte(out), &links)
	var names []string
	for _, l := range links {
		if l.LinkInfo.Kind == "veth" && idx[l.LinkIndex] != "" {
			names = append(names, idx[l.LinkIndex])
		}
	}
	return names
}

// parseDomiflist reads `virsh domiflist`: interface (or "-") and MAC.
func parseDomiflist(out string) firewall.Binding {
	var b firewall.Binding
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 5 || strings.HasPrefix(f[0], "Interface") || strings.HasPrefix(f[0], "---") {
			continue
		}
		mac := strings.ToLower(f[len(f)-1])
		if strings.Count(mac, ":") != 5 {
			continue
		}
		b.MACs = append(b.MACs, mac)
		if f[0] != "-" {
			b.Ifaces = append(b.Ifaces, f[0])
		}
	}
	return b
}

// guestBinding finds where a guest is on the network now.
func (s *Server) guestBinding(ctx context.Context, kind, name string, idx map[int]string) firewall.Binding {
	var b firewall.Binding
	switch kind {
	case "vm":
		if vms.Installed() {
			if out, err := sys.RunCtx(ctx, "virsh", "-c", "qemu:///system", "domiflist", name); err == nil {
				b = parseDomiflist(out)
			}
		}
	case "lxc":
		if c, err := lxc.Get(name); err == nil {
			if c.Network.Mode != "none" && c.Network.MAC != "" {
				b.MACs = append(b.MACs, c.Network.MAC)
			}
			if c.State == "running" {
				b.Ifaces = vethPeers(c.PID, idx)
			}
		}
	case "docker":
		if !docker.Available() {
			return b
		}
		var in struct {
			State struct {
				Pid int `json:"Pid"`
			} `json:"State"`
			NetworkSettings struct {
				Networks map[string]struct {
					MacAddress string `json:"MacAddress"`
				} `json:"Networks"`
			} `json:"NetworkSettings"`
		}
		if s.docker.Call(ctx, "GET", "/containers/"+url.PathEscape(name)+"/json", nil, nil, &in) == nil {
			for _, n := range in.NetworkSettings.Networks {
				if n.MacAddress != "" {
					b.MACs = append(b.MACs, strings.ToLower(n.MacAddress))
				}
			}
			b.Ifaces = vethPeers(in.State.Pid, idx)
		}
	}
	return b
}

// fwEnv gathers what the rules depend on.
func (s *Server) fwEnv(ctx context.Context, c firewall.Config, resolve bool) firewall.Env {
	env := firewall.Env{NAS: nasAddrs()}
	if p, err := strconv.Atoi(s.httpPort); err == nil {
		env.WebPorts = append(env.WebPorts, p)
	}
	env.WebPorts = append(env.WebPorts, 7070, 7443, 22)
	s.store.Read(func(st *store.State) {
		for _, m := range st.Cluster.Members {
			for _, a := range []string{m.Addr, m.PrivAddr, hostOnly(m.Host)} {
				if net.ParseIP(a) != nil {
					env.Peers = append(env.Peers, a)
				}
			}
		}
	})
	for _, v := range s.netConfigNow().Virtual {
		env.Peers = append(env.Peers, v.Peers...)
		if v.Isolate {
			env.VNets = append(env.VNets, v.FW())
		}
	}
	fwState.Lock()
	old := fwState.bindings
	fwState.Unlock()
	env.Bindings = map[string]firewall.Binding{}
	idx := map[int]string{}
	if resolve {
		idx = hostIfaces()
	}
	for _, g := range c.Guests {
		if !g.Enabled {
			continue
		}
		if b, ok := old[g.Key()]; ok && !resolve {
			env.Bindings[g.Key()] = b
			continue
		}
		env.Bindings[g.Key()] = s.guestBinding(ctx, g.Kind, g.Name, idx)
	}
	fwState.Lock()
	fwState.bindings = env.Bindings
	fwState.Unlock()
	return env
}

// hostOnly drops the port of host:port.
func hostOnly(h string) string {
	if a, _, err := net.SplitHostPort(h); err == nil {
		return a
	}
	return strings.Trim(h, "[]")
}

// fwApply renders and loads the tables when they changed (or vanished).
func (s *Server) fwApply(ctx context.Context, resolve bool) error {
	c := s.fwConfig()
	rs := firewall.Render(c, s.fwEnv(ctx, c, resolve))
	script := firewall.Script(rs)
	fwState.Lock()
	same := script == fwState.applied
	fwState.Unlock()
	if same && firewall.Present(rs) {
		return nil
	}
	err := firewall.Apply(rs)
	fwState.Lock()
	defer fwState.Unlock()
	if err != nil {
		if fwState.err != err.Error() {
			slog.Warn("firewall", "err", err)
			s.store.Log("error", "network", "", "Firewall rules could not be loaded: "+err.Error())
		}
		fwState.err = err.Error()
		fwState.applied = ""
		return err
	}
	fwState.err = ""
	fwState.applied = script
	return nil
}

// netFingerprint changes when an interface appears or goes (a guest starts or stops).
func netFingerprint() string {
	entries, _ := os.ReadDir("/sys/class/net")
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Name())
		b.WriteByte(' ')
	}
	return b.String()
}

// fwLoop applies the firewall at start, again within about two seconds of
// a guest starting (new tap or veth), and every minute (addresses of the NAS,
// tables flushed by another tool).
func (s *Server) fwLoop() {
	ctx := context.Background()
	_ = s.fwApply(ctx, true)
	s.vnetDockerForward()
	last, fp := time.Now(), netFingerprint()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		kicked := false
		select {
		case <-t.C:
		case <-fwState.kick:
			kicked = true
		}
		now := netFingerprint()
		if kicked || now != fp || time.Since(last) > time.Minute {
			fp, last = now, time.Now()
			_ = s.fwApply(ctx, true)
			s.vnetDockerForward()
			s.vnetDockerSync(ctx)
		}
	}
}

type fwGuestInfo struct {
	Kind     string           `json:"kind"`
	Name     string           `json:"name"`
	State    string           `json:"state"`
	Bridges  []string         `json:"bridges"`
	Firewall bool             `json:"firewall"`
	Rules    int              `json:"rules"`
	Isolate  bool             `json:"isolate"`
	Binding  firewall.Binding `json:"binding"`
	Missing  bool             `json:"missing,omitempty"` // a firewall is kept for a guest that is gone
}

// fwGuests lists the VMs and containers with their firewall state.
func (s *Server) fwGuests(ctx context.Context, c firewall.Config) []fwGuestInfo {
	out := []fwGuestInfo{}
	seen := map[string]bool{}
	add := func(kind, name, state string, bridges []string) {
		g := c.Guest(kind, name)
		fwState.Lock()
		b := fwState.bindings[g.Key()]
		fwState.Unlock()
		if bridges == nil {
			bridges = []string{}
		}
		out = append(out, fwGuestInfo{Kind: kind, Name: name, State: state, Bridges: bridges, Firewall: g.Enabled, Rules: len(g.Rules), Isolate: g.Isolate, Binding: b})
		seen[g.Key()] = true
	}
	if vms.Installed() {
		if names, err := sys.RunCtx(ctx, "virsh", "-c", "qemu:///system", "list", "--all", "--name"); err == nil {
			for _, n := range strings.Fields(names) {
				state := "shut off"
				if st, err := sys.RunCtx(ctx, "virsh", "-c", "qemu:///system", "domstate", n); err == nil {
					state = strings.TrimSpace(st)
				}
				var bridges []string
				if l, err := sys.RunCtx(ctx, "virsh", "-c", "qemu:///system", "domiflist", n); err == nil {
					for _, line := range strings.Split(l, "\n") {
						if f := strings.Fields(line); len(f) >= 5 && (f[1] == "bridge" || f[1] == "network") {
							bridges = append(bridges, f[2])
						}
					}
				}
				add("vm", n, state, bridges)
			}
		}
	}
	if lxc.Installed() {
		for _, ct := range lxc.List() {
			var bridges []string
			switch ct.Network.Mode {
			case "nat":
				bridges = []string{lxc.NATBridge}
			case "bridge":
				bridges = []string{ct.Network.Bridge}
			}
			add("lxc", ct.Name, ct.State, bridges)
		}
	}
	if docker.Available() {
		var list []struct {
			Names           []string `json:"Names"`
			State           string   `json:"State"`
			NetworkSettings struct {
				Networks map[string]any `json:"Networks"`
			} `json:"NetworkSettings"`
		}
		if s.docker.Call(ctx, "GET", "/containers/json", url.Values{"all": {"1"}}, nil, &list) == nil {
			for _, ct := range list {
				if len(ct.Names) == 0 {
					continue
				}
				var nets []string
				for n := range ct.NetworkSettings.Networks {
					nets = append(nets, n)
				}
				slices.Sort(nets)
				add("docker", strings.TrimPrefix(ct.Names[0], "/"), ct.State, nets)
			}
		}
	}
	for _, g := range c.Guests {
		if !seen[g.Key()] {
			out = append(out, fwGuestInfo{Kind: g.Kind, Name: g.Name, State: "missing", Bridges: []string{}, Firewall: g.Enabled, Rules: len(g.Rules), Isolate: g.Isolate, Missing: true})
		}
	}
	return out
}

// fwSave stores a configuration and applies it.
func (s *Server) fwSave(ctx context.Context, c firewall.Config) error {
	if err := s.store.Update(func(st *store.State) error {
		cc := cloneFW(&c)
		st.Firewall = &cc
		return nil
	}); err != nil {
		return err
	}
	return s.fwApply(ctx, true)
}

// fwRollback ends a trial: the saved configuration comes back.
func (s *Server) fwRollback(reason string) bool {
	fwState.Lock()
	t := fwState.trial
	if t != nil {
		t.timer.Stop()
		fwState.trial = nil
	}
	fwState.Unlock()
	if t == nil {
		return false
	}
	_ = s.fwApply(context.Background(), false)
	msg := "Firewall change rolled back (" + reason + "): the previous rules are active again"
	s.store.Log("warning", "network", "", msg)
	if reason == "timeout" {
		s.notify("warning", "Firewall restored", msg)
	}
	return true
}

func (s *Server) firewallRoutes() {
	s.admin("GET /api/system/network/firewall", func(r *http.Request) (any, error) {
		c := s.fwConfig()
		env := s.fwEnv(r.Context(), c, false)
		fwState.Lock()
		var trial *fwTrial
		if fwState.trial != nil {
			t := *fwState.trial
			trial = &t
		}
		errText := fwState.err
		fwState.Unlock()
		return map[string]any{
			"config": c, "trial": trial, "error": errText, "nft": sys.Has("nft"),
			"guests": s.fwGuests(r.Context(), c), "webPorts": env.WebPorts, "nas": env.NAS, "peers": env.Peers,
			"ruleset": firewall.Render(c, env), "defaultHost": firewall.DefaultHost(),
		}, nil
	})

	// Saves the whole configuration. A change of the NAS firewall is applied
	// on trial: unless confirmed in time, the previous rules come back.
	s.admin("PUT /api/system/network/firewall", func(r *http.Request) (any, error) {
		var req struct {
			Config  firewall.Config `json:"config"`
			Timeout int             `json:"timeout"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := req.Config.Validate(); err != nil {
			return nil, badRequest(err.Error())
		}
		fwState.Lock()
		onTrial := fwState.trial != nil
		fwState.Unlock()
		if onTrial {
			return nil, badRequest("a firewall change is on trial: confirm it or roll it back first")
		}
		old := s.fwConfig()
		env := s.fwEnv(r.Context(), old, false)
		if firewall.Render(old, env).Host == firewall.Render(req.Config, env).Host {
			if err := s.fwSave(r.Context(), req.Config); err != nil {
				return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
			}
			s.audit(r, "network", "Firewall rules changed")
			return map[string]any{"applied": true}, nil
		}
		timeout := time.Duration(req.Timeout) * time.Second
		if timeout < 30*time.Second || timeout > 10*time.Minute {
			timeout = 90 * time.Second
		}
		tok := make([]byte, 24)
		_, _ = rand.Read(tok)
		t := &fwTrial{Token: hex.EncodeToString(tok), Deadline: time.Now().Add(timeout), ReadyAt: time.Now().Add(3 * time.Second), config: cloneFW(&req.Config)}
		fwState.Lock()
		fwState.trial = t
		t.timer = time.AfterFunc(timeout, func() { s.fwRollback("timeout") })
		fwState.Unlock()
		if err := s.fwApply(r.Context(), false); err != nil {
			s.fwRollback("error")
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		s.audit(r, "network", "NAS firewall changed (on trial)")
		return map[string]any{"token": t.Token, "deadline": t.Deadline, "links": []string{}}, nil
	})

	s.admin("POST /api/system/network/firewall/confirm", func(r *http.Request) (any, error) {
		var req struct {
			Token string `json:"token"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		fwState.Lock()
		t := fwState.trial
		switch {
		case t == nil:
			fwState.Unlock()
			return nil, badRequest("no firewall change is waiting for confirmation")
		case subtle.ConstantTimeCompare([]byte(req.Token), []byte(t.Token)) != 1:
			fwState.Unlock()
			return nil, badRequest("invalid confirmation token")
		case time.Now().Before(t.ReadyAt):
			fwState.Unlock()
			return nil, &Error{Status: http.StatusConflict, Msg: network.ErrNotReady.Error()}
		}
		t.timer.Stop()
		fwState.Unlock()
		err := s.store.Update(func(st *store.State) error {
			c := cloneFW(&t.config)
			st.Firewall = &c
			return nil
		})
		fwState.Lock()
		fwState.trial = nil
		fwState.Unlock()
		if err != nil {
			return nil, err
		}
		s.store.Log("info", "network", "", "Firewall change confirmed")
		return nil, nil
	})

	s.admin("POST /api/system/network/firewall/rollback", func(r *http.Request) (any, error) {
		if !s.fwRollback("manual") {
			return nil, badRequest("nothing to roll back")
		}
		return nil, nil
	})

	// One guest's firewall (the Firewall tab of a VM or container).
	s.admin("GET /api/system/network/firewall/guest/{kind}/{name}", func(r *http.Request) (any, error) {
		c := s.fwConfig()
		g := c.Guest(r.PathValue("kind"), r.PathValue("name"))
		fwState.Lock()
		b := fwState.bindings[g.Key()]
		fwState.Unlock()
		if !g.Enabled {
			b = s.guestBinding(r.Context(), g.Kind, g.Name, hostIfaces())
		}
		groups, sets := []string{}, []string{}
		for _, x := range c.Groups {
			groups = append(groups, x.Name)
		}
		for _, x := range c.IPSets {
			sets = append(sets, x.Name)
		}
		if g.Rules == nil {
			g.Rules = []firewall.Rule{}
		}
		return map[string]any{"guest": g, "binding": b, "groups": groups, "sets": sets}, nil
	})
	s.admin("PUT /api/system/network/firewall/guest/{kind}/{name}", func(r *http.Request) (any, error) {
		var g firewall.Guest
		if err := decode(r, &g); err != nil {
			return nil, err
		}
		g.Kind, g.Name = r.PathValue("kind"), r.PathValue("name")
		fwState.Lock()
		onTrial := fwState.trial != nil
		fwState.Unlock()
		if onTrial {
			return nil, badRequest("a firewall change is on trial: confirm it or roll it back first")
		}
		c := s.fwConfig()
		c.SetGuest(g)
		if err := c.Validate(); err != nil {
			return nil, badRequest(err.Error())
		}
		if err := s.fwSave(r.Context(), c); err != nil {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		s.audit(r, "network", "Firewall of "+g.Kind+" "+g.Name+" changed")
		return nil, nil
	})

	s.admin("GET /api/system/network/firewall/log", func(r *http.Request) (any, error) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 1000 {
			limit = 200
		}
		scope := r.URL.Query().Get("scope")
		l := firewall.ReadLog(1000)
		out := []firewall.LogEntry{}
		for _, e := range l {
			if (scope == "" || e.Scope == scope) && len(out) < limit {
				out = append(out, e)
			}
		}
		return out, nil
	})
}
