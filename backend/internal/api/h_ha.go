package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/accounts"
	"strataos/internal/backup"
	"strataos/internal/ha"
	"strataos/internal/jobs"
	"strataos/internal/shares"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// haPairing is the one-time code shown on a NAS that waits to become secondary.
var haPairing struct {
	sync.Mutex
	code    string
	expires time.Time
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func maskHA(c store.HA) store.HA {
	c.Token, c.AuthPass = "", ""
	return c
}

// haPassive reports whether this NAS is paired and does not hold the shared
// address: it must not publish the shares (the other NAS serves them).
func (s *Server) haPassive() bool {
	var role string
	s.store.Read(func(st *store.State) { role = st.HA.Role })
	return role != "" && ha.State() != "MASTER"
}

func (s *Server) peer() (ha.Peer, store.HA) {
	var c store.HA
	s.store.Read(func(st *store.State) { c = st.HA })
	return ha.Peer{Host: c.PeerHost, Token: c.Token, CertFP: c.PeerCert}, c
}

// haTarget is a secondary the primary replicates to.
type haTarget struct {
	Host, Name, Cert string
	Priority         int
	PathMap          map[string]string
}

// haTargets lists the secondaries of a primary, the first one first.
func haTargets(c store.HA) []haTarget {
	var out []haTarget
	if c.PeerHost != "" {
		out = append(out, haTarget{c.PeerHost, c.PeerName, c.PeerCert, 100, c.PathMap})
	}
	for _, x := range c.Extra {
		out = append(out, haTarget{x.Host, x.Name, x.Cert, x.Priority, x.PathMap})
	}
	return out
}

func (t haTarget) peer(token string) ha.Peer {
	return ha.Peer{Host: t.Host, Token: token, CertFP: t.Cert}
}

// mapShare rewrites the paths of a share for a secondary.
func mapShare(sh store.Share, m map[string]string) store.Share {
	sh.Path = ha.MapPath(sh.Path, m)
	if sh.CipherDir != "" {
		sh.CipherDir = ha.MapPath(sh.CipherDir, m)
	}
	return sh
}

// clusterIPs lists every member but one (its unicast VRRP peers).
func clusterIPs(c store.HA, self, except string) []string {
	var out []string
	for _, ip := range append([]string{self}, func() []string {
		var l []string
		for _, t := range haTargets(c) {
			l = append(l, t.Host)
		}
		return l
	}()...) {
		if ip != except && ip != "" {
			out = append(out, ip)
		}
	}
	return out
}

// pushMembers tells every secondary the current members (after a change).
func (s *Server) pushMembers(ctx context.Context, c store.HA) {
	self := ha.SelfIP(c.Interface, c.VIP)
	for _, t := range haTargets(c) {
		_ = t.peer(c.Token).Call(ctx, "members", map[string]any{"peers": clusterIPs(c, self, t.Host)}, nil)
	}
}

func (s *Server) recordTarget(host string, res string) {
	_ = s.store.Update(func(st *store.State) error {
		for i := range st.HA.Extra {
			if st.HA.Extra[i].Host == host {
				st.HA.Extra[i].LastSync, st.HA.Extra[i].LastResult = time.Now(), res
			}
		}
		return nil
	})
}

type haSyncPayload struct {
	Accounts ha.Accounts   `json:"accounts"`
	Users    []store.User  `json:"users"`
	Shares   []store.Share `json:"shares"`
}

// replicate copies accounts, share definitions and data to every secondary
// (paths translated for secondaries whose volumes are named differently).
func (s *Server) replicate(ctx context.Context, logf func(string, ...any)) error {
	_, c := s.peer()
	if c.Role != "primary" {
		return fmt.Errorf("only the primary NAS replicates")
	}
	if ha.State() != "MASTER" {
		return fmt.Errorf("this NAS does not hold the shared address: the secondary is active, hand control back from it first")
	}
	var users []store.User
	var shareList []store.Share
	var names []string
	s.store.Read(func(st *store.State) {
		for _, u := range st.Users {
			if u.Source == "" {
				users = append(users, *u)
				names = append(names, u.Username)
			}
		}
		for _, sh := range st.Shares {
			shareList = append(shareList, *sh)
		}
	})
	var groups []string
	if gl, err := accounts.ListGroups(); err == nil {
		for _, g := range gl {
			groups = append(groups, g.Name)
		}
	}
	groups = append(groups, accounts.BaseGroup)
	acc, err := ha.ExportAccounts(names, groups)
	if err != nil {
		return err
	}
	key := filepath.Join(backup.KeyDir, "id_ed25519")
	known := filepath.Join(backup.KeyDir, "known_hosts")
	var failedAll []string
	for i, t := range haTargets(c) {
		mapped := make([]store.Share, 0, len(shareList))
		for _, sh := range shareList {
			mapped = append(mapped, mapShare(sh, t.PathMap))
		}
		logf("Accounts and shares sent to %s", t.Host)
		var res struct {
			Problems []string `json:"problems"`
		}
		var failed []string
		if err := t.peer(c.Token).Call(ctx, "sync", haSyncPayload{Accounts: acc, Users: users, Shares: mapped}, &res); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", t.Host, err))
		} else {
			for _, pr := range res.Problems {
				logf("%s : %s", t.Host, pr)
			}
			for _, sh := range shareList {
				dst := mapShare(sh, t.PathMap)
				if err := ha.SyncDirTo(ctx, key, known, t.Host, sh.DataPath(), dst.DataPath(), true, logf); err != nil {
					logf("%v", err)
					failed = append(failed, sh.Name)
				}
			}
		}
		if i > 0 { // the first secondary's result is recorded by the caller
			r := "OK"
			if len(failed) > 0 {
				r = "replication failed for " + strings.Join(failed, ", ")
			}
			s.recordTarget(t.Host, r)
		}
		if len(failed) > 0 {
			failedAll = append(failedAll, fmt.Sprintf("%s (%s)", t.Host, strings.Join(failed, ", ")))
		}
	}
	if len(failedAll) > 0 {
		return fmt.Errorf("replication failed for %s", strings.Join(failedAll, "; "))
	}
	return nil
}

func (s *Server) runReplication(user string) (jobs.Snapshot, error) {
	if _, busy := running.LoadOrStore("ha-sync", true); busy {
		return jobs.Snapshot{}, &Error{Status: http.StatusConflict, Msg: "a replication is already running"}
	}
	return s.jobs.Start("ha", "Replication to the secondary NAS", user, func(ctx context.Context, j *jobs.Job) error {
		defer running.Delete("ha-sync")
		err := s.replicate(ctx, j.Logf)
		msg := "OK"
		if err != nil {
			msg = err.Error()
		}
		_ = s.store.Update(func(st *store.State) error {
			st.HA.LastSync, st.HA.LastResult = time.Now(), msg
			return nil
		})
		return err
	}), nil
}

// haLoop follows the VRRP state: the NAS that holds the shared address
// publishes the shares; the primary also replicates on schedule.
func (s *Server) haLoop() {
	last := ha.State()
	tick := 0
	for {
		time.Sleep(2 * time.Second)
		tick++
		var c store.HA
		s.store.Read(func(st *store.State) { c = st.HA })
		if c.Role == "" {
			last = ""
			continue
		}
		cur := ha.State()
		if cur != last {
			slog.Info("ha state", "from", last, "to", cur)
			if cur == "MASTER" && c.Role == "secondary" {
				msg := fmt.Sprintf("Failover: the secondary NAS takes over address %s (the primary %s no longer answers)", c.VIP, c.PeerHost)
				s.store.Log("error", "ha", "", msg)
				s.notify("error", "High availability failover", msg)
			} else if cur == "MASTER" {
				s.store.Log("info", "ha", "", "This NAS serves the shared address "+c.VIP)
			} else if last == "MASTER" {
				s.store.Log("warning", "ha", "", "This NAS no longer serves the shared address "+c.VIP)
			}
			last = cur
			_ = s.applyShares()
		}
		if c.Role == "primary" && cur == "MASTER" && c.Maintenance {
			_ = s.store.Update(func(st *store.State) error { st.HA.Maintenance = false; return nil })
			s.store.Log("info", "ha", "", "Maintenance finished: this NAS serves the shared address again")
		}
		// Planned maintenance: give the address back as soon as the primary is back.
		if tick%5 == 0 && c.Role == "secondary" && cur == "MASTER" && c.AutoReturn {
			p, _ := s.peer()
			var st struct {
				State string `json:"state"`
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if p.Call(ctx, "status", nil, &st) == nil && st.State == "BACKUP" {
				s.store.Log("info", "ha", "", "The primary is back: automatic hand back after maintenance")
				_, _ = s.startHandBack("ha")
			}
			cancel()
		}
		if tick%30 == 0 && c.Role == "primary" && cur == "MASTER" && time.Since(c.LastSync) >= time.Duration(c.Interval)*time.Minute {
			if _, err := s.runReplication("scheduler"); err != nil {
				slog.Debug("ha replication", "err", err)
			}
		}
	}
}

func (s *Server) haRoutes() {
	s.admin("GET /api/ha", func(r *http.Request) (any, error) {
		var c store.HA
		s.store.Read(func(st *store.State) { c = st.HA })
		out := map[string]any{"installed": ha.Installed(), "config": maskHA(c), "state": ha.State()}
		if c.Role == "primary" {
			out["continuous"] = haContSnapshot()
			type sec struct {
				Host       string            `json:"host"`
				Name       string            `json:"name"`
				Priority   int               `json:"priority"`
				PathMap    map[string]string `json:"pathMap,omitempty"`
				LastSync   time.Time         `json:"lastSync"`
				LastResult string            `json:"lastResult"`
				State      string            `json:"state"`
			}
			targets := haTargets(c)
			list := make([]sec, len(targets))
			var wg sync.WaitGroup
			for i, t := range targets {
				list[i] = sec{Host: t.Host, Name: t.Name, Priority: t.Priority, PathMap: t.PathMap, LastSync: c.LastSync, LastResult: c.LastResult}
				if i > 0 {
					list[i].LastSync, list[i].LastResult = c.Extra[i-1].LastSync, c.Extra[i-1].LastResult
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
					defer cancel()
					var st struct {
						State string `json:"state"`
					}
					if t.peer(c.Token).Call(ctx, "status", nil, &st) == nil {
						list[i].State = st.State
					} else {
						list[i].State = "UNREACHABLE"
					}
				}()
			}
			wg.Wait()
			out["secondaries"] = list
		}
		haPairing.Lock()
		if haPairing.code != "" && time.Now().Before(haPairing.expires) {
			out["pairingCode"] = haPairing.code
			out["pairingExpires"] = haPairing.expires
		}
		haPairing.Unlock()
		if c.Role == "" {
			// Interfaces with an IPv4 address, to choose where the shared address lives.
			ifs := map[string]string{}
			ipOut, _ := sys.Run("ip", "-4", "-o", "addr", "show")
			for _, l := range strings.Split(ipOut, "\n") {
				f := strings.Fields(l)
				if len(f) > 3 && f[1] != "lo" && !strings.HasPrefix(f[1], "docker") && !strings.HasPrefix(f[1], "br-") && !strings.HasPrefix(f[1], "virbr") && !strings.HasPrefix(f[1], "veth") {
					ifs[f[1]] = f[3]
				}
			}
			out["interfaces"] = ifs
		}
		return out, nil
	})

	// The future secondary shows a one-time code to type on the primary.
	s.admin("POST /api/ha/prepare", func(r *http.Request) (any, error) {
		var role string
		s.store.Read(func(st *store.State) { role = st.HA.Role })
		if role != "" {
			return nil, badRequest("this NAS is already paired")
		}
		haPairing.Lock()
		defer haPairing.Unlock()
		c := strings.ToUpper(randHex(5))
		haPairing.code = c[:5] + "-" + c[5:]
		haPairing.expires = time.Now().Add(15 * time.Minute)
		s.audit(r, "ha", "Waiting to be paired as secondary")
		return map[string]any{"code": haPairing.code, "expires": haPairing.expires}, nil
	})

	s.admin("POST /api/ha/pair", func(r *http.Request) (any, error) {
		var req struct {
			store.HA
			Code string `json:"code"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var cur store.HA
		s.store.Read(func(st *store.State) { cur = st.HA })
		if cur.Role == "primary" {
			return s.addSecondary(r, cur, req.PeerHost, req.Code, req.PathMap)
		}
		if cur.Role != "" {
			return nil, badRequest("this NAS is already paired")
		}
		c := req.HA
		c.Extra, c.Peers, c.Priority = nil, []string{c.PeerHost}, 0
		if err := validPathMap(c.PathMap); err != nil {
			return nil, badRequest(err.Error())
		}
		if !ha.Installed() {
			return nil, badRequest("install the high availability component first")
		}
		if err := ha.Validate(&c); err != nil {
			return nil, badRequest(err.Error())
		}
		self := ha.SelfIP(c.Interface, c.VIP)
		if self == "" {
			return nil, badRequest("interface " + c.Interface + " has no IPv4 address")
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		fp, err := ha.CertFingerprint(ctx, c.PeerHost)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		pub, err := backup.PublicKey()
		if err != nil {
			return nil, err
		}
		host, _ := hostname()
		c.Role, c.Token, c.AuthPass, c.PeerCert, c.PairedAt = "primary", randHex(32), randHex(4), fp, time.Now()
		var res struct {
			Name      string `json:"name"`
			PublicKey string `json:"publicKey"`
		}
		join := map[string]any{"code": strings.ToUpper(strings.TrimSpace(req.Code)), "primaryHost": self, "primaryName": host, "publicKey": pub, "vip": c.VIP, "vrid": c.VRID, "authPass": c.AuthPass, "token": c.Token, "interval": c.Interval, "priority": 100, "peers": []string{self}, "pathMap": c.PathMap}
		if err := (ha.Peer{Host: c.PeerHost, CertFP: fp}).Call(ctx, "join", join, &res); err != nil {
			return nil, badRequest("pairing refused by " + c.PeerHost + ": " + err.Error())
		}
		if err := ha.AuthorizeKey(res.PublicKey); err != nil {
			return nil, err
		}
		c.PeerName = res.Name
		if err := s.store.Update(func(st *store.State) error { st.HA = c; return nil }); err != nil {
			return nil, err
		}
		if err := ha.ApplyKeepalived(c); err != nil {
			return nil, err
		}
		s.audit(r, "ha", "Paired with "+c.PeerHost+" as primary, shared address "+c.VIP)
		go func() {
			// First replication once this NAS holds the address.
			for i := 0; i < 30 && ha.State() != "MASTER"; i++ {
				time.Sleep(time.Second)
			}
			_, _ = s.runReplication(userOf(r))
		}()
		return maskHA(c), nil
	})

	s.admin("POST /api/ha/sync", func(r *http.Request) (any, error) { return s.runReplication(userOf(r)) })

	// Replication mode of the primary: continuous or not, and the period of
	// the full replication.
	s.admin("PUT /api/ha/settings", func(r *http.Request) (any, error) {
		var req struct {
			Continuous bool `json:"continuous"`
			Interval   int  `json:"interval"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Interval < 1 || req.Interval > 1440 {
			return nil, badRequest("invalid replication period")
		}
		err := s.store.Update(func(st *store.State) error {
			if st.HA.Role != "primary" {
				return badRequest("only the primary replicates")
			}
			st.HA.Continuous, st.HA.Interval = req.Continuous, req.Interval
			return nil
		})
		if err == nil {
			s.audit(r, "ha", fmt.Sprintf("Replication: continuous=%v, full every %d min", req.Continuous, req.Interval))
		}
		return nil, err
	})

	// On the secondary while it is active: copy the data back to the primary,
	// then release the shared address so the primary takes it again.
	s.admin("POST /api/ha/switchback", func(r *http.Request) (any, error) {
		_, c := s.peer()
		if c.Role != "secondary" || ha.State() != "MASTER" {
			return nil, badRequest("only an active secondary can hand control back")
		}
		s.audit(r, "ha", "Handing control back to "+c.PeerHost)
		return s.startHandBack(userOf(r))
	})

	// On the active primary: final replication, then hand the address over to
	// the secondary for a planned reboot; it comes back automatically.
	s.admin("POST /api/ha/maintenance", func(r *http.Request) (any, error) {
		p, c := s.peer()
		if c.Role != "primary" || ha.State() != "MASTER" {
			return nil, badRequest("only the active primary can enter maintenance")
		}
		s.audit(r, "ha", "Maintenance: handing the address over to "+c.PeerHost)
		return s.jobs.Start("ha", "Primary NAS maintenance", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			j.Logf("Last replication before maintenance…")
			if err := s.replicate(ctx, j.Logf); err != nil {
				return err
			}
			if err := p.Call(ctx, "maintenance", map[string]bool{"autoReturn": true}, nil); err != nil {
				return fmt.Errorf("the secondary does not answer: %w", err)
			}
			_ = s.store.Update(func(st *store.State) error { st.HA.Maintenance = true; return nil })
			j.Logf("The secondary takes over; this NAS can be restarted.")
			_, err := sys.Run("systemctl", "stop", "keepalived.service")
			return err
		}), nil
	})

	// Ends the maintenance without rebooting: the primary takes part in VRRP
	// again and the secondary hands back automatically.
	s.admin("POST /api/ha/maintenance/end", func(r *http.Request) (any, error) {
		var c store.HA
		s.store.Read(func(st *store.State) { c = st.HA })
		if c.Role != "primary" {
			return nil, badRequest("not a primary")
		}
		s.audit(r, "ha", "Maintenance finished")
		_, err := sys.Run("systemctl", "start", "keepalived.service")
		return nil, err
	})

	// Removes one secondary of the primary (the others stay paired).
	s.admin("DELETE /api/ha/secondaries/{host}", func(r *http.Request) (any, error) {
		host := r.PathValue("host")
		var c store.HA
		s.store.Read(func(st *store.State) { c = st.HA })
		if c.Role != "primary" {
			return nil, badRequest("not a primary")
		}
		targets := haTargets(c)
		idx := -1
		for i, t := range targets {
			if t.Host == host {
				idx = i
			}
		}
		if idx < 0 {
			return nil, notFound("secondary not found")
		}
		if len(targets) == 1 {
			return nil, badRequest("this is the only secondary: unpair instead")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		warn := ""
		if err := targets[idx].peer(c.Token).Call(ctx, "unpair", nil, nil); err != nil {
			warn = host + " could not be reached: unpair it there too"
		}
		targets = append(targets[:idx], targets[idx+1:]...)
		err := s.store.Update(func(st *store.State) error {
			setTargets(&st.HA, targets)
			c = st.HA
			return nil
		})
		if err != nil {
			return nil, err
		}
		_ = ha.ApplyKeepalived(c)
		s.pushMembers(ctx, c)
		s.audit(r, "ha", "Secondary removed: "+host)
		if warn != "" {
			return map[string]string{"warning": warn}, nil
		}
		return nil, nil
	})

	s.admin("POST /api/ha/unpair", func(r *http.Request) (any, error) {
		p, c := s.peer()
		if c.Role == "" {
			return nil, badRequest("not paired")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		warn := ""
		if c.Role == "primary" {
			for _, t := range haTargets(c) {
				if err := t.peer(c.Token).Call(ctx, "unpair", nil, nil); err != nil {
					warn = t.Host + " could not be reached: unpair it there too"
				}
			}
		} else if err := p.Call(ctx, "unpair", nil, nil); err != nil {
			warn = "the other NAS could not be reached: unpair it there too"
		}
		s.haReset()
		s.audit(r, "ha", "Unpaired from "+c.PeerHost)
		if warn != "" {
			return map[string]string{"warning": warn}, nil
		}
		return nil, nil
	})

	// ---- Calls from the other NAS (authenticated by the pairing code, then the shared token) ----

	peerOK := func(r *http.Request) bool {
		var c store.HA
		s.store.Read(func(st *store.State) { c = st.HA })
		t := r.Header.Get("X-Strata-HA")
		return c.Role != "" && c.Token != "" && subtle.ConstantTimeCompare([]byte(t), []byte(c.Token)) == 1
	}
	peerHandle := func(pattern string, needToken bool, h func(r *http.Request) (any, error)) {
		s.mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
			if needToken && !peerOK(r) {
				writeErr(w, &Error{Status: http.StatusUnauthorized, Msg: "unknown NAS"})
				return
			}
			v, err := h(r)
			if err != nil {
				writeErr(w, err)
				return
			}
			if v == nil {
				v = map[string]bool{"ok": true}
			}
			writeJSON(w, http.StatusOK, v)
		}))
	}

	peerHandle("POST /api/ha/peer/join", false, func(r *http.Request) (any, error) {
		ip := clientIP(r)
		if !s.limiter.attempt(ip) {
			return nil, &Error{Status: http.StatusTooManyRequests, Msg: "too many attempts"}
		}
		var req struct {
			Code        string            `json:"code"`
			PrimaryHost string            `json:"primaryHost"`
			PrimaryName string            `json:"primaryName"`
			PublicKey   string            `json:"publicKey"`
			VIP         string            `json:"vip"`
			VRID        int               `json:"vrid"`
			AuthPass    string            `json:"authPass"`
			Token       string            `json:"token"`
			Interval    int               `json:"interval"`
			Priority    int               `json:"priority"`
			Peers       []string          `json:"peers"`
			PathMap     map[string]string `json:"pathMap"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		haPairing.Lock()
		valid := haPairing.code != "" && time.Now().Before(haPairing.expires) && subtle.ConstantTimeCompare([]byte(req.Code), []byte(haPairing.code)) == 1
		if valid {
			haPairing.code = ""
		}
		haPairing.Unlock()
		if !valid {
			s.store.Log("warning", "ha", "", "Rejected pairing attempt from "+ip)
			return nil, &Error{Status: http.StatusForbidden, Msg: "wrong or expired pairing code"}
		}
		s.limiter.reset(ip)
		if len(req.Token) != 64 || len(req.AuthPass) < 8 {
			return nil, badRequest("invalid pairing data")
		}
		if err := validPathMap(req.PathMap); err != nil {
			return nil, badRequest(err.Error())
		}
		for _, to := range req.PathMap {
			if !s.pathOnVolume(to) {
				return nil, badRequest("no volume at " + to + " on this NAS")
			}
		}
		if req.Priority < 1 || req.Priority > 140 {
			req.Priority = 100
		}
		if len(req.Peers) == 0 {
			req.Peers = []string{req.PrimaryHost}
		}
		c := store.HA{Role: "secondary", PeerHost: req.PrimaryHost, PeerName: req.PrimaryName, VIP: req.VIP, VRID: req.VRID, AuthPass: req.AuthPass[:8], Token: req.Token, Interval: req.Interval, PairedAt: time.Now(), Priority: req.Priority, Peers: req.Peers, PathMap: req.PathMap}
		c.Interface = ha.InterfaceFor(c.VIP)
		if c.Interface == "" {
			return nil, badRequest("this NAS has no interface in the network of " + c.VIP)
		}
		if err := ha.Validate(&c); err != nil {
			return nil, badRequest(err.Error())
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		fp, err := ha.CertFingerprint(ctx, c.PeerHost)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		c.PeerCert = fp
		if err := ha.AuthorizeKey(req.PublicKey); err != nil {
			return nil, badRequest(err.Error())
		}
		pub, err := backup.PublicKey()
		if err != nil {
			return nil, err
		}
		if err := s.store.Update(func(st *store.State) error { st.HA = c; return nil }); err != nil {
			return nil, err
		}
		if err := ha.ApplyKeepalived(c); err != nil {
			return nil, err
		}
		_ = s.applyShares()
		s.store.Log("info", "ha", "", "Paired as secondary of "+c.PeerHost+" ("+c.PeerName+")")
		host, _ := hostname()
		return map[string]string{"name": host, "publicKey": pub}, nil
	})

	peerHandle("POST /api/ha/peer/status", true, func(r *http.Request) (any, error) {
		host, _ := hostname()
		return map[string]string{"state": ha.State(), "name": host}, nil
	})

	peerHandle("POST /api/ha/peer/sync", true, func(r *http.Request) (any, error) {
		var req haSyncPayload
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, badRequest("invalid data")
		}
		if ha.State() == "MASTER" {
			return nil, &Error{Status: http.StatusConflict, Msg: "the secondary is active: hand control back before replicating"}
		}
		problems := ha.ImportAccounts(req.Accounts)
		// Checked before taking the store lock (pathOnVolume reads the store).
		var missing []string
		for _, sh := range req.Shares {
			if !s.pathOnVolume(sh.Path) {
				missing = append(missing, sh.Name)
			}
		}
		err := s.store.Update(func(st *store.State) error {
			for _, u := range req.Users {
				u := u
				if x := st.User(u.Username); x != nil {
					*x = u
				} else {
					st.Users = append(st.Users, &u)
				}
			}
			var list []*store.Share
			for _, sh := range req.Shares {
				sh := sh
				list = append(list, &sh)
			}
			st.Shares = list
			return nil
		})
		if err != nil {
			return nil, err
		}
		for _, n := range missing {
			problems = append(problems, "share "+n+": its volume does not exist on this NAS (check the volume mapping)")
		}
		var list []store.Share
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				list = append(list, *sh)
			}
		})
		for _, sh := range list {
			if s.pathOnVolume(sh.Path) {
				if err := shares.Prepare(&sh); err != nil {
					problems = append(problems, "share "+sh.Name+": "+err.Error())
				}
			}
		}
		_ = s.applyShares()
		_ = s.store.Update(func(st *store.State) error {
			st.HA.LastSync, st.HA.LastResult = time.Now(), "OK"
			if len(problems) > 0 {
				st.HA.LastResult = strings.Join(problems, "; ")
			}
			return nil
		})
		if problems == nil {
			problems = []string{}
		}
		return map[string]any{"problems": problems}, nil
	})

	peerHandle("POST /api/ha/peer/maintenance", true, func(r *http.Request) (any, error) {
		err := s.store.Update(func(st *store.State) error { st.HA.AutoReturn = true; return nil })
		s.store.Log("info", "ha", "", "Maintenance of the primary: this NAS serves the shares until it is back")
		return nil, err
	})

	// The members of the cluster changed (a secondary was added or removed).
	peerHandle("POST /api/ha/peer/members", true, func(r *http.Request) (any, error) {
		var req struct {
			Peers []string `json:"peers"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Peers) == 0 || len(req.Peers) > 16 {
			return nil, badRequest("invalid members")
		}
		var c store.HA
		same := false
		err := s.store.Update(func(st *store.State) error {
			same = slices.Equal(st.HA.Peers, req.Peers)
			st.HA.Peers = req.Peers
			c = st.HA
			return nil
		})
		if err != nil || same {
			return nil, err // unchanged: no restart (a secondary would wait 30 s again)
		}
		return nil, ha.ApplyKeepalived(c)
	})

	peerHandle("POST /api/ha/peer/unpair", true, func(r *http.Request) (any, error) {
		s.haReset()
		s.store.Log("warning", "ha", "", "Unpaired by the other NAS")
		return nil, nil
	})
}

// startHandBack copies the changes made on the active secondary to the
// primary, then releases the shared address so the primary takes it again.
func (s *Server) startHandBack(user string) (jobs.Snapshot, error) {
	if _, busy := running.LoadOrStore("ha-handback", true); busy {
		return jobs.Snapshot{}, &Error{Status: http.StatusConflict, Msg: "already handing back"}
	}
	p, c := s.peer()
	return s.jobs.Start("ha", "Handing back to the primary NAS", user, func(ctx context.Context, j *jobs.Job) error {
		defer running.Delete("ha-handback")
		var st struct {
			State string `json:"state"`
		}
		if err := p.Call(ctx, "status", nil, &st); err != nil {
			return fmt.Errorf("the primary %s does not answer: %w", c.PeerHost, err)
		}
		var list []store.Share
		s.store.Read(func(x *store.State) {
			for _, sh := range x.Shares {
				list = append(list, *sh)
			}
		})
		key := filepath.Join(backup.KeyDir, "id_ed25519")
		known := filepath.Join(backup.KeyDir, "known_hosts")
		j.Logf("Copying the changes to %s…", c.PeerHost)
		for _, sh := range list {
			// Back to the primary's paths (its volumes may be named differently).
			if err := ha.SyncDirTo(ctx, key, known, c.PeerHost, sh.DataPath(), ha.MapPath(sh.DataPath(), ha.Reverse(c.PathMap)), true, j.Logf); err != nil {
				return err
			}
		}
		_ = s.store.Update(func(x *store.State) error { x.HA.AutoReturn = false; return nil })
		j.Logf("Releasing address %s", c.VIP)
		return ha.Release()
	}), nil
}

func (s *Server) haReset() {
	_ = s.store.Update(func(st *store.State) error { st.HA = store.HA{}; return nil })
	_ = ha.RevokeKey()
	_ = ha.ApplyKeepalived(store.HA{})
	_ = s.applyShares()
}

// setTargets writes back the list of secondaries (the first in the main fields).
func setTargets(c *store.HA, targets []haTarget) {
	extra := map[string]store.HASecondary{}
	for _, x := range c.Extra {
		extra[x.Host] = x
	}
	if len(targets) == 0 {
		c.PeerHost, c.PeerName, c.PeerCert, c.PathMap, c.Extra = "", "", "", nil, nil
		return
	}
	first := targets[0]
	if first.Host != c.PeerHost { // the first secondary changed: carry its results over
		x := extra[first.Host]
		c.LastSync, c.LastResult = x.LastSync, x.LastResult
	}
	c.PeerHost, c.PeerName, c.PeerCert, c.PathMap = first.Host, first.Name, first.Cert, first.PathMap
	c.Extra = nil
	for _, t := range targets[1:] {
		x := extra[t.Host]
		c.Extra = append(c.Extra, store.HASecondary{Host: t.Host, Name: t.Name, Cert: t.Cert, Priority: t.Priority, PathMap: t.PathMap, LastSync: x.LastSync, LastResult: x.LastResult})
	}
	c.Peers = nil
	for _, t := range targets {
		c.Peers = append(c.Peers, t.Host)
	}
}

// validPathMap checks a volume mapping (mount points under the storage).
func validPathMap(m map[string]string) error {
	if len(m) > 64 {
		return fmt.Errorf("too many mapped volumes")
	}
	for from, to := range m {
		for _, p := range []string{from, to} {
			if filepath.Clean(p) != p || !strings.HasPrefix(p, "/srv/strata/") || strings.Count(p, "/") > 4 {
				return fmt.Errorf("invalid volume path %q", p)
			}
		}
	}
	return nil
}

// addSecondary pairs one more secondary with the active primary.
func (s *Server) addSecondary(r *http.Request, c store.HA, host, code string, pathMap map[string]string) (any, error) {
	if err := validPathMap(pathMap); err != nil {
		return nil, badRequest(err.Error())
	}
	targets := haTargets(c)
	if len(targets) >= 8 {
		return nil, badRequest("too many secondaries")
	}
	for _, t := range targets {
		if t.Host == host {
			return nil, badRequest(host + " is already a secondary")
		}
	}
	self := ha.SelfIP(c.Interface, c.VIP)
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	fp, err := ha.CertFingerprint(ctx, host)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	pub, err := backup.PublicKey()
	if err != nil {
		return nil, err
	}
	// Each new secondary comes after the others (lower VRRP priority).
	prio := 100 - 10*len(targets)
	peers := append([]string{self}, func() []string {
		var l []string
		for _, t := range targets {
			l = append(l, t.Host)
		}
		return l
	}()...)
	me, _ := hostname()
	var res struct {
		Name      string `json:"name"`
		PublicKey string `json:"publicKey"`
	}
	join := map[string]any{"code": strings.ToUpper(strings.TrimSpace(code)), "primaryHost": self, "primaryName": me, "publicKey": pub, "vip": c.VIP, "vrid": c.VRID, "authPass": c.AuthPass, "token": c.Token, "interval": c.Interval, "priority": prio, "peers": peers, "pathMap": pathMap}
	if err := (ha.Peer{Host: host, CertFP: fp}).Call(ctx, "join", join, &res); err != nil {
		return nil, badRequest("pairing refused by " + host + ": " + err.Error())
	}
	if err := ha.AuthorizeKey(res.PublicKey); err != nil {
		return nil, err
	}
	targets = append(targets, haTarget{Host: host, Name: res.Name, Cert: fp, Priority: prio, PathMap: pathMap})
	err = s.store.Update(func(st *store.State) error {
		setTargets(&st.HA, targets)
		c = st.HA
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := ha.ApplyKeepalived(c); err != nil {
		return nil, err
	}
	s.pushMembers(ctx, c)
	s.audit(r, "ha", fmt.Sprintf("Secondary added: %s (priority %d)", host, prio))
	go func() { _, _ = s.runReplication(userOf(r)) }()
	return maskHA(c), nil
}
