package api

// Watched NAS: StrataOS NAS outside the cluster that this console watches
// read-only (status summary only, no updates, no VM transfers). They pair
// exactly like cluster members (single-use code, pinned certificate,
// console token) but are kept in st.Fleet.Watched, never in Members.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/store"
)

// watchedRow is what the console shows for one watched NAS.
type watchedRow struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Host     string         `json:"host"`
	Online   bool           `json:"online"`
	Error    string         `json:"error,omitempty"`
	LastSeen time.Time      `json:"lastSeen,omitzero"`
	Added    time.Time      `json:"added"`
	Summary  map[string]any `json:"summary,omitempty"`
}

// watchedProbe asks one watched NAS for its summary (read-only peer route).
func watchedProbe(ctx context.Context, w store.FleetWatched) (map[string]any, error) {
	m := store.FleetMember{ID: w.ID, Name: w.Name, Host: w.Host, Cert: w.Cert, Token: w.Token}
	var sum map[string]any
	if err := fleetCall(ctx, m, http.MethodGet, "/api/fleet/peer/summary", nil, &sum); err != nil {
		return nil, err
	}
	return sum, nil
}

// applyWatchedResult records the outcome of one probe in the state.
func applyWatchedResult(st *store.State, id string, sum map[string]any, probeErr error) {
	for i := range st.Fleet.Watched {
		if st.Fleet.Watched[i].ID != id {
			continue
		}
		w := &st.Fleet.Watched[i]
		if probeErr != nil {
			var pe *peerError
			if errors.As(probeErr, &pe) {
				w.LastError = fmt.Sprintf("HTTP %d: %s", pe.Status, pe.Msg)
			} else {
				w.LastError = probeErr.Error()
			}
			w.Summary = nil
			return
		}
		w.LastSeen = time.Now()
		w.LastError = ""
		w.Summary = sum
		return
	}
}

// watchedView lists the watched NAS with their last known state.
func (s *Server) watchedView() []watchedRow {
	var list []store.FleetWatched
	s.store.Read(func(st *store.State) { list = slices.Clone(st.Fleet.Watched) })
	out := make([]watchedRow, len(list))
	for i, w := range list {
		out[i] = watchedRow{ID: w.ID, Name: w.Name, Host: w.Host, LastSeen: w.LastSeen, Added: w.Added, Summary: w.Summary}
		if w.LastError != "" {
			out[i].Error = w.LastError
		} else if !w.LastSeen.IsZero() {
			out[i].Online = true
		}
	}
	return out
}

// watchedRefreshAll probes every watched NAS (in parallel) and stores the
// results; it returns the rows as they are after the probes.
func (s *Server) watchedRefreshAll(ctx context.Context) []watchedRow {
	var list []store.FleetWatched
	s.store.Read(func(st *store.State) { list = slices.Clone(st.Fleet.Watched) })
	var wg sync.WaitGroup
	for _, w := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			sum, err := watchedProbe(cctx, w)
			_ = s.store.Update(func(st *store.State) error {
				applyWatchedResult(st, w.ID, sum, err)
				return nil
			})
		}()
	}
	wg.Wait()
	return s.watchedView()
}

// watchedLoop keeps the watched summaries fresh in the background.
func (s *Server) watchedLoop() {
	time.Sleep(2 * time.Minute)
	for {
		n := 0
		s.store.Read(func(st *store.State) { n = len(st.Fleet.Watched) })
		if n > 0 {
			s.watchedRefreshAll(context.Background())
		}
		time.Sleep(10 * time.Minute)
	}
}

func (s *Server) fleetWatchedRoutes() {
	s.admin("GET /api/fleet/watched", func(r *http.Request) (any, error) {
		if r.URL.Query().Get("refresh") == "1" {
			return s.watchedRefreshAll(r.Context()), nil
		}
		return s.watchedView(), nil
	})
	s.admin("POST /api/fleet/watched", func(r *http.Request) (any, error) {
		var req struct {
			Host string `json:"host"`
			Code string `json:"code"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		host, err := validFleetHost(req.Host)
		if err != nil {
			return nil, err
		}
		code := strings.TrimSpace(req.Code)
		if code == "" {
			return nil, badRequest("the authorization code is required")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		name, token, fp, err := fleetJoin(ctx, host, code)
		if err != nil {
			return nil, err
		}
		w := store.FleetWatched{ID: newLinkID()[:12], Name: name, Host: host, Cert: fp, Token: token, Added: time.Now()}
		err = s.store.Update(func(st *store.State) error {
			if len(st.Fleet.Watched) >= 200 {
				return badRequest("too many NAS")
			}
			for _, x := range st.Fleet.Watched {
				if x.Host == host {
					return badRequest("this NAS is already watched")
				}
			}
			st.Fleet.Watched = append(st.Fleet.Watched, w)
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "fleet", "watched NAS added (read-only): "+host)
		// First probe right away, so the list shows something useful.
		go s.watchedRefreshAll(context.Background())
		return map[string]string{"id": w.ID, "name": w.Name}, nil
	})
	s.admin("POST /api/fleet/watched/refresh", func(r *http.Request) (any, error) {
		return s.watchedRefreshAll(r.Context()), nil
	})
	s.admin("DELETE /api/fleet/watched/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		s.audit(r, "fleet", "watched NAS removed")
		return nil, s.store.Update(func(st *store.State) error {
			st.Fleet.Watched = slices.DeleteFunc(st.Fleet.Watched, func(w store.FleetWatched) bool { return w.ID == id })
			return nil
		})
	})
}
