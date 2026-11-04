package api

// Console side of the VM and container transfers between NAS (see
// h_fleet_xfer.go): plan, start, follow, resume and cancel.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"strataos/internal/backup"
	"strataos/internal/jobs"
	"strataos/internal/store"
)

// xferEnd is one NAS of a transfer: this one (local) or a member.
type xferEnd struct {
	s     *Server
	local bool
	m     store.FleetMember
	name  string
}

func (s *Server) xferEndOf(id string) (xferEnd, error) {
	if id == "local" {
		return xferEnd{s: s, local: true, name: sysHostname()}, nil
	}
	m, ok := s.fleetMember(id)
	if !ok {
		return xferEnd{}, badRequest("unknown NAS")
	}
	return xferEnd{s: s, m: m, name: m.Name}, nil
}

func (e xferEnd) call(ctx context.Context, op string, q xferReq, out any) error {
	if e.local {
		v, err := e.s.xferOp("local", "", op, q)
		if err != nil || out == nil {
			return err
		}
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	}
	return fleetCall(ctx, e.m, http.MethodPost, "/api/fleet/peer/xfer/"+op, q, out)
}

// fleetRaw sends a chunk call to a member (5 minutes for 8 MB on a slow link).
func fleetRaw(ctx context.Context, m store.FleetMember, method, path string, body []byte) ([]byte, http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, fleetURL(m)+path, rd)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.Token)
	req.Header.Set("Content-Type", "application/octet-stream")
	tr := backup.PinnedTransport(m.Cert)
	tr.DisableKeepAlives = true
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2*xferChunk+1<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return nil, nil, &peerError{Status: resp.StatusCode, Msg: firstNonEmpty(e.Error, "HTTP "+resp.Status)}
	}
	return data, resp.Header, nil
}

func chunkPath(id, file string, off int64) string {
	return "/api/fleet/peer/xfer-chunk/" + id + "?file=" + url.QueryEscape(file) + "&off=" + strconv.FormatInt(off, 10)
}

func (e xferEnd) read(ctx context.Context, id, file string, off int64) ([]byte, int, error) {
	if e.local {
		x, err := xferOwned(id, "local")
		if err != nil {
			return nil, 0, err
		}
		return e.s.xferRead(x, file, off)
	}
	gz, h, err := fleetRaw(ctx, e.m, http.MethodGet, chunkPath(id, file, off), nil)
	if err != nil {
		return nil, 0, err
	}
	n, err := strconv.Atoi(h.Get("X-Strata-Len"))
	if err != nil || n <= 0 || n > xferChunk {
		return nil, 0, errors.New("invalid chunk")
	}
	return gz, n, nil
}

func (e xferEnd) write(ctx context.Context, id, file string, off int64, gz []byte) (int64, error) {
	if e.local {
		x, err := xferOwned(id, "local")
		if err != nil {
			return 0, err
		}
		return e.s.xferWrite(x, file, off, gz)
	}
	data, _, err := fleetRaw(ctx, e.m, http.MethodPut, chunkPath(id, file, off), gz)
	if err != nil {
		return 0, err
	}
	var res struct {
		Have int64 `json:"have"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return 0, err
	}
	return res.Have, nil
}

// retryable: network errors, overloaded or failing NAS (not refusals).
func retryable(err error) bool {
	var pe *peerError
	if errors.As(err, &pe) {
		return pe.Status >= 500 || pe.Status == http.StatusTooManyRequests
	}
	var e *Error
	if errors.As(err, &e) {
		return false
	}
	return !errors.Is(err, context.Canceled)
}

func withRetry[T any](ctx context.Context, logf func(string, ...any), what string, fn func() (T, error)) (T, error) {
	var zero T
	for n := 1; ; n++ {
		v, err := fn()
		if err == nil || !retryable(err) || n >= xferRetryAttempts || ctx.Err() != nil {
			return v, err
		}
		d := xferRetryDelay(n)
		logf("%s: %v (new try in %s)", what, err, d)
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(d):
		}
	}
}

// ---- Records ----

var xferEditMu sync.Mutex

func (s *Server) xferRecord(id string) (store.FleetTransfer, bool) {
	var t store.FleetTransfer
	s.store.Read(func(st *store.State) {
		for _, x := range st.Fleet.Transfers {
			if x.ID == id {
				t = x
				t.Bridges = cloneMap(x.Bridges)
			}
		}
	})
	return t, t.ID != ""
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *Server) xferEdit(id string, fn func(t *store.FleetTransfer)) {
	xferEditMu.Lock()
	defer xferEditMu.Unlock()
	_ = s.store.Update(func(st *store.State) error {
		for i := range st.Fleet.Transfers {
			if st.Fleet.Transfers[i].ID == id {
				fn(&st.Fleet.Transfers[i])
			}
		}
		return nil
	})
}

// ---- Plan ----

type xferStartReq struct {
	From    string            `json:"from"`
	To      string            `json:"to"`
	Kind    string            `json:"kind"`
	Name    string            `json:"name"`
	NewName string            `json:"newName"`
	Mode    string            `json:"mode"`
	Move    bool              `json:"move"`
	Storage string            `json:"storage"`
	Bridges map[string]string `json:"bridges"`
	Start   bool              `json:"start"`
}

type xferPlanView struct {
	Issues  []xferPlanIssue `json:"issues"`
	Object  *xferObj        `json:"object,omitempty"`
	Bridges []string        `json:"bridges"` // of the destination
	Volumes []xferVolume    `json:"volumes"` // of the destination
}

func (s *Server) xferPlan(ctx context.Context, q *xferStartReq) (xferPlanView, error) {
	out := xferPlanView{Issues: []xferPlanIssue{}, Bridges: []string{}, Volumes: []xferVolume{}}
	if q.Kind != "vm" && q.Kind != "lxc" {
		return out, badRequest("invalid kind")
	}
	if q.NewName == "" {
		q.NewName = q.Name
	}
	if q.Mode == "" {
		q.Mode = "stop"
	}
	src, err := s.xferEndOf(q.From)
	if err != nil {
		return out, err
	}
	dst, err := s.xferEndOf(q.To)
	if err != nil {
		return out, err
	}
	var si, di xferInventory
	var serr, derr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); serr = src.call(ctx, "inventory", xferReq{}, &si) }()
	go func() { defer wg.Done(); derr = dst.call(ctx, "inventory", xferReq{}, &di) }()
	wg.Wait()
	if serr != nil {
		return out, badRequest(fmt.Sprintf("%s: %v", src.name, serr))
	}
	if derr != nil {
		return out, badRequest(fmt.Sprintf("%s: %v", dst.name, derr))
	}
	out.Bridges, out.Volumes = di.Bridges, di.Volumes
	list, dlist, ready := si.VMs, di.VMs, di.Libvirt
	if q.Kind == "lxc" {
		list, dlist, ready = si.Containers, di.Containers, di.LXC
	}
	i := slices.IndexFunc(list, func(o xferObj) bool { return o.Name == q.Name })
	if i < 0 {
		return out, notFound(fmt.Sprintf("%s not found on %s", q.Name, src.name))
	}
	obj := list[i]
	out.Object = &obj
	var names []string
	for _, o := range dlist {
		names = append(names, o.Name)
	}
	var vols []string
	for _, v := range di.Volumes {
		vols = append(vols, v.Path)
	}
	out.Issues = xferPlanIssues(xferPlanInput{Kind: q.Kind, SrcBridges: obj.Bridges, SrcState: obj.State, SrcRBD: obj.Ceph, Mode: q.Mode, Move: q.Move,
		NewName: q.NewName, Storage: q.Storage, Bridges: q.Bridges, DstReady: ready, DstNames: names, DstBridges: di.Bridges, DstVolumes: vols,
		SameNAS: q.From == q.To || (si.Name != "" && si.Name == di.Name && q.From != "local" && q.To != "local")})
	if out.Issues == nil {
		out.Issues = []xferPlanIssue{}
	}
	q.Bridges = xferBridges(obj.Bridges, q.Bridges)
	return out, nil
}

// ---- The console's job ----

func (s *Server) startXferJob(t store.FleetTransfer, user string) jobs.Snapshot {
	verb := "Copying"
	if t.Move {
		verb = "Moving"
	}
	title := fmt.Sprintf("%s %s %s from %s to %s", verb, kindLabel(t.Kind), t.Name, t.FromName, t.ToName)
	snap := s.jobs.Start("fleet", title, user, func(ctx context.Context, j *jobs.Job) error {
		err := s.xferRun(ctx, t, j)
		now := time.Now()
		switch {
		case err == nil:
			s.xferEdit(t.ID, func(x *store.FleetTransfer) {
				x.Status, x.Stage, x.Progress, x.Error, x.Finished = "success", "finish", 100, "", &now
			})
		case ctx.Err() != nil:
			// Cancelled: both sides put things back.
			j.Logf("Cancelled: removing the copy on both NAS")
			for _, id := range []string{t.From, t.To} {
				if e, eerr := s.xferEndOf(id); eerr == nil {
					c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					_ = e.call(c, "abort", xferReq{ID: t.ID}, nil)
					cancel()
				}
			}
			s.xferEdit(t.ID, func(x *store.FleetTransfer) { x.Status, x.Error, x.Finished = "cancelled", "", &now })
		default:
			s.xferEdit(t.ID, func(x *store.FleetTransfer) { x.Status, x.Error, x.Finished = "failed", err.Error(), &now })
			s.store.Log("error", "fleet", "", fmt.Sprintf("Transfer of %s %s from %s to %s failed: %v", kindLabel(t.Kind), t.Name, t.FromName, t.ToName, err))
		}
		return err
	})
	s.xferEdit(t.ID, func(x *store.FleetTransfer) { x.Job = snap.ID })
	return snap
}

// waitSide polls a side until its stage is one of want (or failed).
func waitSide(ctx context.Context, e xferEnd, id string, want []string, progress func(float64), logf func(string, ...any)) (xferView, error) {
	var v xferView
	fails := 0
	for {
		err := e.call(ctx, "state", xferReq{ID: id}, &v)
		switch {
		case err != nil && retryable(err) && fails < 20:
			fails++
		case err != nil:
			return v, fmt.Errorf("%s: %w", e.name, err)
		default:
			fails = 0
			if slices.Contains(want, v.Stage) {
				return v, nil
			}
			if v.Stage == "failed" {
				msg := v.Error
				if v.Job != nil && v.Job.Error != "" {
					msg = v.Job.Error
				}
				if v.Job != nil && v.Job.Log != "" {
					logf("%s: %s", e.name, lastLines(v.Job.Log, 5))
				}
				return v, fmt.Errorf("%s: %s", e.name, msg)
			}
			if v.Job != nil && v.Job.Progress >= 0 {
				progress(v.Job.Progress / 100)
			}
		}
		select {
		case <-ctx.Done():
			return v, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func lastLines(s string, n int) string {
	lines := bytes.Split(bytes.TrimSpace([]byte(s)), []byte("\n"))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return string(bytes.Join(lines, []byte(" / ")))
}

func (s *Server) xferRun(ctx context.Context, t store.FleetTransfer, j *jobs.Job) error {
	src, err := s.xferEndOf(t.From)
	if err != nil {
		return err
	}
	dst, err := s.xferEndOf(t.To)
	if err != nil {
		return err
	}
	q := xferReq{ID: t.ID, Kind: t.Kind, Name: t.Name, NewName: t.NewName, Mode: t.Mode, Move: t.Move, Storage: t.Storage, Bridges: t.Bridges, Start: t.Start}
	var last time.Time
	report := func(stage string, frac float64, bytes, total int64, force bool) {
		p := xferProgress(stage, frac)
		j.SetProgress(p)
		if force || time.Since(last) > 3*time.Second {
			last = time.Now()
			s.xferEdit(t.ID, func(x *store.FleetTransfer) {
				x.Stage, x.Progress = stage, p
				if total > 0 {
					x.Bytes, x.Total = bytes, total
				}
			})
		}
	}

	// 1. The copy on the source.
	report("export", 0, 0, 0, true)
	qs := q
	qs.Peer = dst.name
	j.Logf("Preparing the copy on %s", src.name)
	var sv xferView
	if err := src.call(ctx, "export", qs, &sv); err != nil {
		return fmt.Errorf("%s: %w", src.name, err)
	}
	if sv.Stage != "ready" {
		if sv, err = waitSide(ctx, src, t.ID, []string{"ready"}, func(f float64) { report("export", f, 0, 0, false) }, j.Logf); err != nil {
			return err
		}
	}
	total, _ := xferTotal(sv.Files)
	j.Logf("Copy ready on %s: %d MB", src.name, total>>20)

	// 2. The destination gets ready.
	qd := q
	qd.Peer, qd.Files = src.name, sv.Files
	var dv xferView
	if err := dst.call(ctx, "receive", qd, &dv); err != nil {
		return fmt.Errorf("%s: %w", dst.name, err)
	}
	if dv.Stage != "done" {
		// 3. The files, from what the destination already has.
		_, done := xferTotal(dv.Files)
		if done > 0 {
			j.Logf("Resuming: %d MB already on %s", done>>20, dst.name)
		}
		report("transfer", float64(done)/float64(max(total, 1)), done, total, true)
		started, startDone := time.Now(), done
		for _, f := range dv.Files {
			off := min(f.Have, f.Size)
			for off < f.Size {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				type chunk struct {
					gz []byte
					n  int
				}
				c, err := withRetry(ctx, j.Logf, src.name, func() (chunk, error) {
					gz, n, err := src.read(ctx, t.ID, f.Name, off)
					return chunk{gz, n}, err
				})
				if err != nil {
					return fmt.Errorf("%s: %w", src.name, err)
				}
				have, err := withRetry(ctx, j.Logf, dst.name, func() (int64, error) {
					return dst.write(ctx, t.ID, f.Name, off, c.gz)
				})
				var pe *peerError
				var le *Error
				if (errors.As(err, &pe) && pe.Status == http.StatusConflict) || (errors.As(err, &le) && le.Status == http.StatusConflict) {
					// The destination has another length (a call that
					// arrived after its timeout): ask it where to go on.
					var v xferView
					if err := dst.call(ctx, "state", xferReq{ID: t.ID}, &v); err != nil {
						return fmt.Errorf("%s: %w", dst.name, err)
					}
					if v.Stage != "receiving" {
						return fmt.Errorf("%s: %s", dst.name, firstNonEmpty(v.Error, "the destination stopped receiving"))
					}
					i := slices.IndexFunc(v.Files, func(x xferFile) bool { return x.Name == f.Name })
					if i < 0 {
						return fmt.Errorf("%s: file %s unknown", dst.name, f.Name)
					}
					done += v.Files[i].Have - off
					off = v.Files[i].Have
					continue
				}
				if err != nil {
					return fmt.Errorf("%s: %w", dst.name, err)
				}
				done += have - off
				off = have
				report("transfer", float64(done)/float64(max(total, 1)), done, total, false)
			}
		}
		if secs := time.Since(started).Seconds(); secs > 1 {
			j.Logf("%d MB sent in %.0f s (%.1f MB/s)", (done-startDone)>>20, secs, float64(done-startDone)/secs/(1<<20))
		}
		report("transfer", 1, total, total, true)

		// 4. Verified and restored on the destination.
		j.Logf("Verifying and restoring on %s", dst.name)
		if err := dst.call(ctx, "commit", xferReq{ID: t.ID}, &dv); err != nil {
			return fmt.Errorf("%s: %w", dst.name, err)
		}
		if _, err := waitSide(ctx, dst, t.ID, []string{"done"}, func(f float64) { report("restore", (f*100-85)/15, total, total, false) }, j.Logf); err != nil {
			return err
		}
	}

	// 5. The source of a move is deleted, the temporary files removed.
	report("finish", 0, total, total, true)
	if t.Move {
		j.Logf("Deleting %s on %s", t.Name, src.name)
	}
	if err := src.call(ctx, "finish", xferReq{ID: t.ID}, nil); err != nil {
		j.Logf("%s: %v", src.name, err)
		if t.Move {
			return fmt.Errorf("%s: the copy is on %s but the source could not be deleted: %w", src.name, dst.name, err)
		}
	}
	if err := dst.call(ctx, "finish", xferReq{ID: t.ID}, nil); err != nil {
		j.Logf("%s: %v", dst.name, err)
	}
	j.Logf("Done")
	return nil
}

// ---- Routes ----

func (s *Server) fleetXferRoutes() {
	s.fleetXferPeerRoutes()

	s.admin("GET /api/fleet/xfer/inventory", func(r *http.Request) (any, error) {
		e, err := s.xferEndOf(firstNonEmpty(r.URL.Query().Get("nas"), "local"))
		if err != nil {
			return nil, err
		}
		var inv xferInventory
		if err := e.call(r.Context(), "inventory", xferReq{}, &inv); err != nil {
			var pe *peerError
			if errors.As(err, &pe) && pe.Status == http.StatusForbidden {
				return nil, &Error{Status: http.StatusForbidden, Msg: e.name + ": " + pe.Msg}
			}
			return nil, badRequest(e.name + ": " + err.Error())
		}
		return inv, nil
	})
	s.admin("POST /api/fleet/transfers/plan", func(r *http.Request) (any, error) {
		var q xferStartReq
		if err := decode(r, &q); err != nil {
			return nil, err
		}
		return s.xferPlan(r.Context(), &q)
	})
	s.admin("GET /api/fleet/transfers", func(r *http.Request) (any, error) {
		out := []store.FleetTransfer{}
		s.store.Read(func(st *store.State) {
			for i := len(st.Fleet.Transfers) - 1; i >= 0; i-- {
				out = append(out, st.Fleet.Transfers[i])
			}
		})
		return out, nil
	})
	s.admin("POST /api/fleet/transfers", func(r *http.Request) (any, error) {
		var q xferStartReq
		if err := decode(r, &q); err != nil {
			return nil, err
		}
		plan, err := s.xferPlan(r.Context(), &q)
		if err != nil {
			return nil, err
		}
		if len(plan.Issues) > 0 {
			return nil, badRequest(plan.Issues[0].Text)
		}
		rq := xferReq{ID: randHex(8), Kind: q.Kind, Name: q.Name, NewName: q.NewName, Mode: q.Mode, Move: q.Move, Storage: q.Storage, Bridges: q.Bridges}
		if err := checkXferReq(&rq); err != nil {
			return nil, err
		}
		src, _ := s.xferEndOf(q.From)
		dst, _ := s.xferEndOf(q.To)
		t := store.FleetTransfer{ID: rq.ID, Kind: q.Kind, Name: q.Name, NewName: rq.NewName, From: q.From, To: q.To, FromName: src.name, ToName: dst.name,
			Mode: rq.Mode, Move: q.Move, Storage: q.Storage, Bridges: rq.Bridges, Start: q.Start, User: userOf(r), Status: "running", Stage: "export", Started: time.Now()}
		err = s.store.Update(func(st *store.State) error {
			for _, x := range st.Fleet.Transfers {
				if x.Status == "running" && x.Kind == t.Kind && ((x.From == t.From && x.Name == t.Name) || (x.To == t.To && x.NewName == t.NewName)) {
					return &Error{Status: http.StatusConflict, Msg: "a transfer of this " + kindLabel(t.Kind) + " is already running"}
				}
			}
			st.Fleet.Transfers = append(st.Fleet.Transfers, t)
			if n := len(st.Fleet.Transfers); n > xferMaxTransfers {
				st.Fleet.Transfers = st.Fleet.Transfers[n-xferMaxTransfers:]
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		verb := "Copy"
		if t.Move {
			verb = "Move"
		}
		s.audit(r, "fleet", fmt.Sprintf("%s of %s %s from %s to %s started", verb, kindLabel(t.Kind), t.Name, t.FromName, t.ToName))
		s.startXferJob(t, userOf(r))
		t, _ = s.xferRecord(t.ID)
		return t, nil
	})
	s.admin("POST /api/fleet/transfers/{id}/resume", func(r *http.Request) (any, error) {
		t, ok := s.xferRecord(r.PathValue("id"))
		if !ok {
			return nil, notFound("transfer not found")
		}
		if t.Status != "failed" {
			return nil, &Error{Status: http.StatusConflict, Msg: "only a failed transfer can resume"}
		}
		s.xferEdit(t.ID, func(x *store.FleetTransfer) { x.Status, x.Error, x.Finished = "running", "", nil })
		s.audit(r, "fleet", fmt.Sprintf("Transfer of %s %s resumed", kindLabel(t.Kind), t.Name))
		s.startXferJob(t, userOf(r))
		t, _ = s.xferRecord(t.ID)
		return t, nil
	})
	s.admin("POST /api/fleet/transfers/{id}/cancel", func(r *http.Request) (any, error) {
		t, ok := s.xferRecord(r.PathValue("id"))
		if !ok {
			return nil, notFound("transfer not found")
		}
		s.audit(r, "fleet", fmt.Sprintf("Transfer of %s %s cancelled", kindLabel(t.Kind), t.Name))
		switch t.Status {
		case "running":
			if j, ok := s.jobs.Get(t.Job); ok && j.Status == jobs.Running {
				s.jobs.Cancel(t.Job)
				return nil, nil
			}
			fallthrough
		case "failed":
			// Put things back on both NAS (a moved source runs again).
			return s.jobs.Start("fleet", fmt.Sprintf("Cancelling the transfer of %s %s", kindLabel(t.Kind), t.Name), userOf(r), func(ctx context.Context, j *jobs.Job) error {
				var errs []error
				for _, id := range []string{t.From, t.To} {
					e, err := s.xferEndOf(id)
					if err == nil {
						err = e.call(ctx, "abort", xferReq{ID: t.ID}, nil)
					}
					var pe *peerError
					if err != nil && !(errors.As(err, &pe) && pe.Status == http.StatusNotFound) {
						errs = append(errs, fmt.Errorf("%s: %w", e.name, err))
					}
				}
				now := time.Now()
				s.xferEdit(t.ID, func(x *store.FleetTransfer) { x.Status, x.Finished = "cancelled", &now })
				return errors.Join(errs...)
			}), nil
		}
		return nil, &Error{Status: http.StatusConflict, Msg: "this transfer is over"}
	})
	s.admin("DELETE /api/fleet/transfers/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		return nil, s.store.Update(func(st *store.State) error {
			i := slices.IndexFunc(st.Fleet.Transfers, func(x store.FleetTransfer) bool { return x.ID == id })
			if i < 0 {
				return notFound("transfer not found")
			}
			if st.Fleet.Transfers[i].Status == "running" || st.Fleet.Transfers[i].Status == "failed" {
				return &Error{Status: http.StatusConflict, Msg: "cancel the transfer first"}
			}
			st.Fleet.Transfers = slices.Delete(st.Fleet.Transfers, i, i+1)
			return nil
		})
	})
}
