package api

// Multi-NAS console: this NAS shows the health of other StrataOS NAS. Each
// one allows it with a single-use code and gets back a token, read-only
// unless that NAS also lets the console install its updates
// (h_fleet_updates.go); calls go over HTTPS with the certificate pinned at
// pairing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/backup"
	"strataos/internal/ha"
	"strataos/internal/store"
)

var fleetCodes struct {
	sync.Mutex
	m map[string]time.Time
}

// fleetSummary is what a watched NAS tells about itself.
func (s *Server) fleetSummary() map[string]any {
	host, _ := hostname()
	ov := s.overview()
	h, _ := s.monitor.History()
	var last any
	if len(h) > 0 {
		last = h[len(h)-1]
	}
	var ok, failed int
	var lastFail string
	s.store.Read(func(st *store.State) {
		for _, b := range st.Backups {
			if len(b.History) == 0 {
				continue
			}
			if b.History[0].Status == "success" {
				ok++
			} else {
				failed++
				lastFail = b.Name
			}
		}
	})
	ov["hostname"], ov["version"], ov["uptimeDays"], ov["sample"] = host, s.version, uptimeDays(), last
	ov["backups"] = map[string]any{"ok": ok, "failed": failed, "lastFailure": lastFail}
	ov["updates"] = s.updateInfo(false)
	if ha := s.fleetHA(); ha != nil {
		ov["ha"] = ha
	}
	if c := s.clusterState(); c.ID != "" && len(c.Members) > 1 {
		ov["cluster"] = c.ID
	}
	return ov
}

func (s *Server) fleetRoutes() {
	// ---- On a watched NAS ----
	s.fleetPeerRoutes()
	s.fleetUpdateRoutes()
	s.admin("POST /api/fleet/code", func(r *http.Request) (any, error) {
		code := pairingCode()
		fleetCodes.Lock()
		if fleetCodes.m == nil {
			fleetCodes.m = map[string]time.Time{}
		}
		fleetCodes.m[code] = time.Now().Add(15 * time.Minute)
		fleetCodes.Unlock()
		return map[string]any{"code": code}, nil
	})
	s.admin("GET /api/fleet/managers", func(r *http.Request) (any, error) {
		out := []store.FleetManager{}
		s.store.Read(func(st *store.State) {
			for _, m := range st.Fleet.Managers {
				m.TokenHash = ""
				out = append(out, m)
			}
		})
		return out, nil
	})
	s.admin("DELETE /api/fleet/managers/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		s.audit(r, "fleet", "Monitoring access revoked")
		return nil, s.store.Update(func(st *store.State) error {
			st.Fleet.Managers = slices.DeleteFunc(st.Fleet.Managers, func(m store.FleetManager) bool { return m.ID == id })
			return nil
		})
	})
	s.mux.HandleFunc("POST /api/fleet-join", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		ip := clientIP(r)
		if !s.limiter.attempt(ip) {
			writeErr(w, &Error{Status: http.StatusTooManyRequests, Msg: "too many attempts, try again later"})
			return
		}
		var req struct {
			Code string `json:"code"`
			Name string `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		code := strings.ToUpper(strings.TrimSpace(req.Code))
		fleetCodes.Lock()
		exp, ok := fleetCodes.m[code]
		delete(fleetCodes.m, code)
		fleetCodes.Unlock()
		if !ok || time.Now().After(exp) {
			s.guardFail(ip, "console multi-NAS", "")
			writeErr(w, &Error{Status: http.StatusForbidden, Msg: "wrong or expired pairing code"})
			return
		}
		s.limiter.reset(ip)
		name := strings.TrimSpace(req.Name)
		if name == "" || len(name) > 64 {
			name = ip
		}
		token := randHex(32)
		_ = s.store.Update(func(st *store.State) error {
			st.Fleet.Managers = append(st.Fleet.Managers, store.FleetManager{ID: newLinkID()[:12], Name: name, TokenHash: hashToken(token), Created: time.Now()})
			return nil
		})
		host, _ := hostname()
		s.store.Log("info", "fleet", "", fmt.Sprintf("The NAS %s can now monitor this NAS", name))
		writeJSON(w, http.StatusOK, map[string]string{"token": token, "name": host})
	})

	// ---- On the console NAS ----
	s.admin("GET /api/fleet", func(r *http.Request) (any, error) {
		var members []store.FleetMember
		s.store.Read(func(st *store.State) { members = slices.Clone(st.Fleet.Members) })
		type row struct {
			ID      string         `json:"id"`
			Name    string         `json:"name"`
			Host    string         `json:"host"`
			Online  bool           `json:"online"`
			Error   string         `json:"error,omitempty"`
			Summary map[string]any `json:"summary,omitempty"`
			HAWith  []string       `json:"haWith,omitempty"` // rows of its HA pair
		}
		out := make([]row, len(members)+1)
		out[0] = row{ID: "local", Name: "", Host: "", Online: true, Summary: s.fleetSummary()}
		out[0].Name, _ = out[0].Summary["hostname"].(string)
		var wg sync.WaitGroup
		for i, m := range members {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rw := row{ID: m.ID, Name: m.Name, Host: m.Host}
				ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
				defer cancel()
				var pe *peerError
				switch err := fleetCall(ctx, m, http.MethodGet, "/api/fleet/peer/summary", nil, &rw.Summary); {
				case errors.As(err, &pe):
					rw.Error = fmt.Sprintf("HTTP %d: %s", pe.Status, pe.Msg)
				case err != nil:
					rw.Error = err.Error()
				default:
					rw.Online = true
				}
				out[i+1] = rw
			}()
		}
		wg.Wait()
		// HA pairs, as the NAS report them.
		nodes := make([]fleetNode, len(out))
		for i, rw := range out {
			nodes[i].Host = rw.Host
			if rw.Summary == nil {
				continue
			}
			nodes[i].Hostname, _ = rw.Summary["hostname"].(string)
			if ha, ok := rw.Summary["ha"].(map[string]any); ok {
				nodes[i].HARole, _ = ha["role"].(string)
				peers, _ := ha["peers"].([]any)
				for _, p := range peers {
					if p, ok := p.(string); ok {
						nodes[i].HAPeers = append(nodes[i].HAPeers, p)
					}
				}
			}
		}
		groups := fleetGroups(nodes)
		for i := range out {
			for j := range out {
				if i != j && groups[i] >= 0 && groups[i] == groups[j] {
					out[i].HAWith = append(out[i].HAWith, out[j].ID)
				}
			}
		}
		return out, nil
	})
	s.admin("POST /api/fleet", func(r *http.Request) (any, error) {
		var req struct {
			Host string `json:"host"`
			Code string `json:"code"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		host := strings.TrimSpace(req.Host)
		if host == "" || strings.ContainsAny(host, "/ @?#") {
			return nil, badRequest("invalid address")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		fp, err := ha.CertFingerprint(ctx, host)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		me, _ := hostname()
		body, _ := json.Marshal(map[string]string{"code": req.Code, "name": me})
		hreq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+bracketHost(host)+":7443/api/fleet-join", strings.NewReader(string(body)))
		hreq.Header.Set("Content-Type", "application/json")
		hreq.Header.Set("X-Strata", "1")
		resp, err := (&http.Client{Transport: backup.PinnedTransport(fp)}).Do(hreq)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		defer resp.Body.Close()
		var res struct {
			Token string `json:"token"`
			Name  string `json:"name"`
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&res)
		if resp.StatusCode != http.StatusOK {
			return nil, badRequest(firstNonEmpty(res.Error, resp.Status))
		}
		m := store.FleetMember{ID: newLinkID()[:12], Name: res.Name, Host: host, Cert: fp, Token: res.Token, Added: time.Now()}
		err = s.store.Update(func(st *store.State) error {
			if len(st.Fleet.Members) >= 200 {
				return badRequest("too many NAS")
			}
			st.Fleet.Members = append(st.Fleet.Members, m)
			return nil
		})
		s.audit(r, "fleet", "NAS added to the console: "+host)
		return map[string]string{"id": m.ID, "name": m.Name}, err
	})
	s.admin("DELETE /api/fleet/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		s.audit(r, "fleet", "NAS removed from the console")
		return nil, s.store.Update(func(st *store.State) error {
			st.Fleet.Members = slices.DeleteFunc(st.Fleet.Members, func(m store.FleetMember) bool { return m.ID == id })
			return nil
		})
	})
}
