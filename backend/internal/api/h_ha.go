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

type haSyncPayload struct {
	Accounts ha.Accounts   `json:"accounts"`
	Users    []store.User  `json:"users"`
	Shares   []store.Share `json:"shares"`
}

// replicate copies accounts, share definitions and data to the secondary.
func (s *Server) replicate(ctx context.Context, logf func(string, ...any)) error {
	p, c := s.peer()
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
	logf("Comptes et partages envoyés à %s", c.PeerHost)
	var res struct {
		Problems []string `json:"problems"`
	}
	if err := p.Call(ctx, "sync", haSyncPayload{Accounts: acc, Users: users, Shares: shareList}, &res); err != nil {
		return fmt.Errorf("%s: %w", c.PeerHost, err)
	}
	for _, pr := range res.Problems {
		logf("%s : %s", c.PeerHost, pr)
	}
	key := filepath.Join(backup.KeyDir, "id_ed25519")
	known := filepath.Join(backup.KeyDir, "known_hosts")
	var failed []string
	for _, sh := range shareList {
		if err := ha.SyncPath(ctx, key, known, c.PeerHost, sh.DataPath(), logf); err != nil {
			logf("%v", err)
			failed = append(failed, sh.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("replication failed for %s", strings.Join(failed, ", "))
	}
	return nil
}

func (s *Server) runReplication(user string) (jobs.Snapshot, error) {
	if _, busy := running.LoadOrStore("ha-sync", true); busy {
		return jobs.Snapshot{}, &Error{Status: http.StatusConflict, Msg: "a replication is already running"}
	}
	return s.jobs.Start("ha", "Réplication vers le NAS secondaire", user, func(ctx context.Context, j *jobs.Job) error {
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
				msg := fmt.Sprintf("Bascule : le NAS secondaire prend le relais sur l'adresse %s (le principal %s ne répond plus)", c.VIP, c.PeerHost)
				s.store.Log("error", "ha", "", msg)
				s.notify("Bascule haute disponibilité", msg)
			} else if cur == "MASTER" {
				s.store.Log("info", "ha", "", "Ce NAS sert l'adresse partagée "+c.VIP)
			} else if last == "MASTER" {
				s.store.Log("warning", "ha", "", "Ce NAS ne sert plus l'adresse partagée "+c.VIP)
			}
			last = cur
			_ = s.applyShares()
		}
		if c.Role == "primary" && cur == "MASTER" && c.Maintenance {
			_ = s.store.Update(func(st *store.State) error { st.HA.Maintenance = false; return nil })
			s.store.Log("info", "ha", "", "Maintenance terminée : ce NAS sert de nouveau l'adresse partagée")
		}
		// Planned maintenance: give the address back as soon as the primary is back.
		if tick%5 == 0 && c.Role == "secondary" && cur == "MASTER" && c.AutoReturn {
			p, _ := s.peer()
			var st struct {
				State string `json:"state"`
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if p.Call(ctx, "status", nil, &st) == nil && st.State == "BACKUP" {
				s.store.Log("info", "ha", "", "Le principal est de retour : retour automatique après la maintenance")
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
		c := req.HA
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
		join := map[string]any{"code": strings.ToUpper(strings.TrimSpace(req.Code)), "primaryHost": self, "primaryName": host, "publicKey": pub, "vip": c.VIP, "vrid": c.VRID, "authPass": c.AuthPass, "token": c.Token, "interval": c.Interval}
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
		return s.jobs.Start("ha", "Maintenance du NAS principal", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			j.Logf("Dernière réplication avant la maintenance…")
			if err := s.replicate(ctx, j.Logf); err != nil {
				return err
			}
			if err := p.Call(ctx, "maintenance", map[string]bool{"autoReturn": true}, nil); err != nil {
				return fmt.Errorf("the secondary does not answer: %w", err)
			}
			_ = s.store.Update(func(st *store.State) error { st.HA.Maintenance = true; return nil })
			j.Logf("Le secondaire prend le relais ; ce NAS peut être redémarré.")
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

	s.admin("POST /api/ha/unpair", func(r *http.Request) (any, error) {
		p, c := s.peer()
		if c.Role == "" {
			return nil, badRequest("not paired")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		warn := ""
		if err := p.Call(ctx, "unpair", nil, nil); err != nil {
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
			Code        string `json:"code"`
			PrimaryHost string `json:"primaryHost"`
			PrimaryName string `json:"primaryName"`
			PublicKey   string `json:"publicKey"`
			VIP         string `json:"vip"`
			VRID        int    `json:"vrid"`
			AuthPass    string `json:"authPass"`
			Token       string `json:"token"`
			Interval    int    `json:"interval"`
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
		c := store.HA{Role: "secondary", PeerHost: req.PrimaryHost, PeerName: req.PrimaryName, VIP: req.VIP, VRID: req.VRID, AuthPass: req.AuthPass[:8], Token: req.Token, Interval: req.Interval, PairedAt: time.Now()}
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
			problems = append(problems, "share "+n+": no volume with the same name on this NAS")
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
	return s.jobs.Start("ha", "Retour au NAS principal", user, func(ctx context.Context, j *jobs.Job) error {
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
		j.Logf("Copie des modifications vers %s…", c.PeerHost)
		for _, sh := range list {
			if err := ha.SyncPath(ctx, key, known, c.PeerHost, sh.DataPath(), j.Logf); err != nil {
				return err
			}
		}
		_ = s.store.Update(func(x *store.State) error { x.HA.AutoReturn = false; return nil })
		j.Logf("Libération de l'adresse %s", c.VIP)
		return ha.Release()
	}), nil
}

func (s *Server) haReset() {
	_ = s.store.Update(func(st *store.State) error { st.HA = store.HA{}; return nil })
	_ = ha.RevokeKey()
	_ = ha.ApplyKeepalived(store.HA{})
	_ = s.applyShares()
}
