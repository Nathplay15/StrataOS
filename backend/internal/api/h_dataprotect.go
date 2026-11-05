package api

// Data protection (0.10): the guided restore of files, folders, shared
// folders or the whole NAS from snapshots, backups and cloud copies; the
// 3-2-1 assessment; the monthly restore tests.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"strataos/internal/confbackup"
	"strataos/internal/jobs"
	"strataos/internal/protect"
	"strataos/internal/restore"
	"strataos/internal/scheduler"
	"strataos/internal/shares"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// restoreSources lists what can be restored from. With forPath, only the
// sources that hold that path.
func (s *Server) restoreSources(forPath string) []restore.Source {
	var out []restore.Source
	covers := func(base string) bool {
		return forPath == "" || forPath == base || strings.HasPrefix(forPath, base+"/") || strings.HasPrefix(base, forPath+"/")
	}
	// Snapshots: Btrfs volumes and ZFS datasets mounted under the storage root.
	seen := map[string]bool{}
	var mounts []string
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			if v.FSType == "btrfs" {
				mounts = append(mounts, v.MountPoint)
			}
		}
	})
	if storage.ZFSAvailable() {
		ds, _ := storage.ListZDatasets("", "filesystem")
		for _, d := range ds {
			if strings.HasPrefix(d.Mountpoint, storage.MountRoot+"/") {
				mounts = append(mounts, d.Mountpoint)
			}
		}
	}
	for _, mp := range mounts {
		if seen[mp] || !storage.IsMountpoint(mp) {
			continue
		}
		seen[mp] = true
		if forPath != "" {
			// The deepest dataset holding the path has its snapshots.
			if m := storage.MountOf(forPath); m != mp {
				continue
			}
		}
		out = append(out, restore.SnapshotSource(mp))
	}
	for _, t := range storage.ThinLVs() {
		if t.MountPoint != "" && strings.HasPrefix(t.MountPoint, storage.MountRoot+"/") && covers(t.MountPoint) {
			out = append(out, restore.ThinSource(t.VG, t.LV, t.MountPoint))
		}
	}
	if forPath == "" {
		out = append(out, s.imageSources()...)
	}
	s.store.Read(func(st *store.State) {
		for _, b := range st.Backups {
			src, err := restore.BackupSource(resolveTask(st, b))
			if err != nil {
				continue
			}
			if forPath != "" && !slices.ContainsFunc(b.Sources, covers) {
				continue
			}
			out = append(out, src)
		}
		for _, c := range st.Clouds {
			src, err := restore.CloudSource(*c)
			if err != nil || !covers(c.LocalPath) {
				continue
			}
			out = append(out, src)
		}
	})
	return out
}

// restoreSource finds a source by its id.
func (s *Server) restoreSource(id string) (restore.Source, error) {
	kind, rest, _ := strings.Cut(id, ":")
	switch kind {
	case "snap":
		mp := filepath.Clean(rest)
		if !strings.HasPrefix(mp, storage.MountRoot+"/") || storage.ShadowMount(mp) != mp {
			return nil, notFound("no snapshots there")
		}
		return restore.SnapshotSource(mp), nil
	case "lvm":
		vg, lv, _ := strings.Cut(rest, "/")
		for _, t := range storage.ThinLVs() {
			if t.VG == vg && t.LV == lv && t.MountPoint != "" {
				return restore.ThinSource(vg, lv, t.MountPoint), nil
			}
		}
	case "backup":
		var t *store.BackupTask
		s.store.Read(func(st *store.State) {
			if x := st.Backup(rest); x != nil {
				c := resolveTask(st, x)
				t = &c
			}
		})
		if t != nil {
			src, err := restore.BackupSource(*t)
			if err != nil {
				return nil, badRequest(err.Error())
			}
			return src, nil
		}
	case "image":
		return s.imageSourceOf(rest)
	case "cloud":
		var t *store.CloudSync
		s.store.Read(func(st *store.State) {
			if x := st.Cloud(rest); x != nil {
				c := *x
				t = &c
			}
		})
		if t != nil {
			src, err := restore.CloudSource(*t)
			if err != nil {
				return nil, badRequest(err.Error())
			}
			return src, nil
		}
	}
	return nil, notFound("restore source not found")
}

// cleanAbs checks an original path given by the browser.
func cleanAbs(p string) (string, error) {
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\n\r") || path.Clean(p) != p {
		return "", badRequest("invalid path")
	}
	return p, nil
}

type restoreRequest struct {
	Source string   `json:"source"`
	Point  string   `json:"point"`
	Paths  []string `json:"paths"`
	Mode   string   `json:"mode"`   // inplace | folder
	Target string   `json:"target"` // folder mode: where
	Policy string   `json:"policy"` // overwrite | skip | rename
}

// restorePlan is a checked restore request.
type restorePlan struct {
	src    restore.Source
	point  string
	items  []protect.Entry
	dest   func(string) string
	target string // the folder written to (folder mode), or "" in place
}

func (s *Server) planRestore(ctx context.Context, req restoreRequest) (*restorePlan, error) {
	src, err := s.restoreSource(req.Source)
	if err != nil {
		return nil, err
	}
	if len(req.Paths) == 0 || len(req.Paths) > 1000 {
		return nil, badRequest("choose what to restore")
	}
	if src.Info().Kind == "image" && req.Mode == "inplace" {
		return nil, badRequest("files of a disk backup are restored to a folder or downloaded")
	}
	p := &restorePlan{src: src, point: req.Point}
	for _, raw := range req.Paths {
		abs, err := cleanAbs(raw)
		if err != nil {
			return nil, err
		}
		e, err := src.Stat(ctx, req.Point, abs)
		if err != nil {
			return nil, notFound(abs + " is not in this version")
		}
		e.Path = abs
		p.items = append(p.items, e)
	}
	// Nested selections would be restored twice.
	for _, a := range p.items {
		for _, b := range p.items {
			if a.Path != b.Path && strings.HasPrefix(b.Path, a.Path+"/") {
				return nil, badRequest(b.Path + " is inside " + a.Path + ": choose one of them")
			}
		}
	}
	var dests []string
	switch req.Mode {
	case "inplace":
		p.dest = func(abs string) string { return abs }
		for _, it := range p.items {
			dests = append(dests, it.Path)
		}
	case "folder":
		t, err := shares.ValidatePath(req.Target)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		if strings.Contains(t+"/", "/.snapshots/") || strings.Contains(t+"/", "/.zfs/") {
			return nil, badRequest("choose a folder outside the snapshots")
		}
		for _, it := range p.items {
			if t == it.Path || strings.HasPrefix(t, it.Path+"/") {
				return nil, badRequest("the destination cannot be inside what is restored")
			}
		}
		p.target = t
		// Each item goes under the target with its own name.
		bases := map[string]string{}
		for _, it := range p.items {
			bases[it.Path] = path.Dir(it.Path)
		}
		items := p.items
		p.dest = func(abs string) string {
			for _, it := range items {
				if abs == it.Path || strings.HasPrefix(abs, it.Path+"/") {
					return protect.Destination(abs, bases[it.Path], t)
				}
			}
			return path.Join(t, path.Base(abs))
		}
		dests = []string{t}
	default:
		return nil, badRequest("choose where to restore")
	}
	for _, d := range dests {
		if !strings.HasPrefix(d, storage.MountRoot+"/") || !s.pathOnVolume(d) {
			return nil, badRequest(d + " is not on a mounted volume: restore to another folder")
		}
	}
	if name := s.lockedShareIn(dests); name != "" {
		return nil, badRequest("the encrypted share " + name + " is locked: unlock it first")
	}
	return p, nil
}

// listing is the recursive content of the selected folders.
func (p *restorePlan) listing(ctx context.Context) ([]protect.Entry, bool, error) {
	var all []protect.Entry
	truncated := false
	for _, it := range p.items {
		if !it.Dir {
			continue
		}
		list, tr, err := p.src.List(ctx, p.point, it.Path, true)
		if err != nil {
			return nil, false, err
		}
		all = append(all, list...)
		truncated = truncated || tr
	}
	return all, truncated, nil
}

// safetySnapshot snapshots the Btrfs volume or ZFS dataset of dir before
// files are overwritten, so the restore itself can be undone.
func safetySnapshot(dir string, logf func(string, ...any)) {
	out, err := sys.Run("findmnt", "-n", "-o", "TARGET,FSTYPE,SOURCE", "-T", dir)
	f := strings.Fields(out)
	if err != nil || len(f) < 3 || !strings.HasPrefix(f[0], storage.MountRoot+"/") {
		return
	}
	var msg string
	switch f[1] {
	case "btrfs":
		msg, err = scheduler.SnapshotBtrfs(f[0], "", "pre-restore-", 5, 0, 0, logf)
	case "zfs":
		msg, err = scheduler.SnapshotZFS(f[2], "pre-restore-", false, 5, 0, 0, logf)
	default:
		return
	}
	if err != nil {
		logf("No safety snapshot: %v", err)
		return
	}
	logf("Safety snapshot before overwriting: %s", msg)
}

// cronInterval is the time between two runs of a cron schedule (0: on demand).
func cronInterval(cron string, now time.Time) time.Duration {
	if c, err := scheduler.ParseCron(cron); err == nil && cron != "" {
		n1 := c.Next(now)
		if !n1.IsZero() {
			return c.Next(n1).Sub(n1)
		}
	}
	return 0
}

func lastOK(h []store.ScheduleRun) (time.Time, bool) {
	failed := len(h) > 0 && h[0].Status != "success"
	for _, r := range h {
		if r.Status == "success" {
			return r.Started, failed
		}
	}
	return time.Time{}, failed
}

// protectionCopies lists the backup tasks that copy this NAS's data.
func (s *Server) protectionCopies(now time.Time) []protect.Copy {
	var bks []store.BackupTask
	var clouds []store.CloudSync
	dsOffsite := map[string]bool{}
	s.store.Read(func(st *store.State) {
		for _, b := range st.Backups {
			if b.DestType == "datastore" {
				if ds := st.Datastore(b.Datastore); ds != nil {
					dsOffsite[b.ID] = ds.Offsite
				}
			}
			bks = append(bks, resolveTask(st, b))
		}
		for _, c := range st.Clouds {
			clouds = append(clouds, *c)
		}
	})
	out := []protect.Copy{}
	health := func(c *protect.Copy, enabled bool, cron string, h []store.ScheduleRun) {
		last, failed := lastOK(h)
		c.LastOK = last
		switch {
		case !enabled:
			c.Why = "disabled"
		case failed:
			c.Why = "last run failed"
		case protect.Overdue(last, cronInterval(cron, now), now):
			c.Why = "no recent success"
		default:
			c.Healthy = true
		}
	}
	for _, b := range bks {
		if b.SourceType != "" || b.Kind == "sync" || len(b.Sources)+len(b.VMs)+len(b.Containers) == 0 || b.DestType == "datastore" {
			continue // pulls copy another NAS; a two-way sync copies deletions too
		}
		dest := b.DestType
		usb := dest == "local" && b.Kind != "zfs" && storage.OnUSB(b.DestPath)
		c := protect.Copy{ID: "backup:" + b.ID, Name: b.Name, Kind: "backup"}
		c.Media, c.Offsite = protect.MediaOf(dest, usb)
		c.Offsite = c.Offsite || b.Offsite || dsOffsite[b.ID]
		health(&c, b.Enabled, b.Cron, b.History)
		out = append(out, c)
	}
	for _, t := range clouds {
		if t.Direction != "upload" {
			continue
		}
		c := protect.Copy{ID: "cloud:" + t.ID, Name: t.Name, Kind: "cloud", Media: protect.MediaCloud, Offsite: true}
		if t.Provider == "sftp" || t.Provider == "webdav" {
			c.Media = protect.MediaRemote
		}
		health(&c, t.Enabled, t.Cron, t.History)
		out = append(out, c)
	}
	return out
}

// testable lists the tasks a restore test can read: id, name, source.
func (s *Server) testable() (ids []string, names map[string]string) {
	names = map[string]string{}
	s.store.Read(func(st *store.State) {
		for _, b := range st.Backups {
			if t, _ := lastOK(b.History); b.Enabled && restore.Browsable(*b) && !t.IsZero() && (b.DestType != "datastore" || len(b.Sources) > 0) {
				ids = append(ids, "backup:"+b.ID)
				names["backup:"+b.ID] = b.Name
			}
		}
		for _, c := range st.Clouds {
			if t, _ := lastOK(c.History); c.Enabled && c.Direction == "upload" && !t.IsZero() {
				ids = append(ids, "cloud:"+c.ID)
				names["cloud:"+c.ID] = c.Name
			}
		}
	})
	return ids, names
}

// runRestoreTests tests every backup (or only one), as a job.
func (s *Server) runRestoreTests(user, only string) (jobs.Snapshot, error) {
	ids, names := s.testable()
	if only != "" {
		if !slices.Contains(ids, only) {
			return jobs.Snapshot{}, badRequest("this backup cannot be tested (it must be enabled, browsable and have run once)")
		}
		ids = []string{only}
	}
	if len(ids) == 0 {
		return jobs.Snapshot{}, badRequest("no backup to test yet")
	}
	if _, busy := running.LoadOrStore("restore-test", true); busy {
		return jobs.Snapshot{}, &Error{Status: http.StatusConflict, Msg: "a restore test is already running"}
	}
	if user == "" {
		user = "scheduler"
	}
	return s.jobs.Start("restoretest", "Restore test", user, func(ctx context.Context, j *jobs.Job) error {
		defer running.Delete("restore-test")
		var failed, passed []string
		for i, id := range ids {
			j.SetProgress(float64(i) * 100 / float64(len(ids)))
			j.Logf("— %s", names[id])
			src, err := s.restoreSource(id)
			var res store.RestoreTest
			if err != nil {
				res = store.RestoreTest{Time: time.Now(), Status: "failed", Message: err.Error()}
			} else {
				tctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
				res = restore.RunTest(tctx, src, restore.Progress{Log: j.Logf})
				cancel()
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			kind, tid, _ := strings.Cut(id, ":")
			_ = s.store.Update(func(st *store.State) error {
				r := res
				if kind == "backup" {
					if x := st.Backup(tid); x != nil {
						x.RestoreTest = &r
					}
				} else if x := st.Cloud(tid); x != nil {
					x.RestoreTest = &r
				}
				return nil
			})
			line := names[id] + ": " + res.Message
			if res.Status == "success" {
				passed = append(passed, "• "+line)
			} else {
				failed = append(failed, "• "+line)
			}
		}
		_ = s.store.Update(func(st *store.State) error {
			st.Protection.LastRestoreTest = time.Now()
			return nil
		})
		go s.runAdvisor(false)
		if len(failed) > 0 {
			msg := strings.Join(append(failed, passed...), "\n")
			s.store.Log("error", "backup", "", fmt.Sprintf("Restore test failed for %d backup(s)", len(failed)))
			s.notify("error", "Restore test failed", msg)
			return fmt.Errorf("restore test failed for %d backup(s)", len(failed))
		}
		s.store.Log("info", "backup", "", fmt.Sprintf("Restore test passed for %d backup(s)", len(passed)))
		s.notify("info", "Restore test passed", strings.Join(passed, "\n"))
		return nil
	}), nil
}

// restoreTestDue runs the restore tests once a month (at 5:20 on the 1st,
// or the next day at 5:20 when the NAS was off).
func (s *Server) restoreTestDue(tick time.Time) bool {
	if tick.Hour() != 5 || tick.Minute() != 20 {
		return false
	}
	var off bool
	var last time.Time
	s.store.Read(func(st *store.State) { off, last = st.Protection.RestoreTestOff, st.Protection.LastRestoreTest })
	if off {
		return false
	}
	return tick.Day() == 1 || tick.Sub(last) > 35*24*time.Hour
}

func (s *Server) dataProtectRoutes() {
	s.admin("GET /api/restore/sources", func(r *http.Request) (any, error) {
		forPath := r.URL.Query().Get("path")
		if forPath != "" {
			if _, err := cleanAbs(forPath); err != nil {
				return nil, err
			}
		}
		out := []restore.Info{}
		for _, src := range s.restoreSources(forPath) {
			out = append(out, src.Info())
		}
		return out, nil
	})

	s.admin("GET /api/restore/points", func(r *http.Request) (any, error) {
		src, err := s.restoreSource(r.URL.Query().Get("source"))
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		pts, err := src.Points(ctx)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return pts, nil
	})

	// One folder of a point: its direct children (path empty: the roots).
	s.admin("GET /api/restore/browse", func(r *http.Request) (any, error) {
		q := r.URL.Query()
		src, err := s.restoreSource(q.Get("source"))
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		dir := q.Get("path")
		if rs, ok := src.(restore.Rooted); ok && dir == "" {
			roots, err := rs.Roots(q.Get("point"))
			if err != nil {
				return nil, badRequest(err.Error())
			}
			out := []protect.Entry{}
			for _, root := range roots {
				out = append(out, protect.Entry{Path: root, Name: path.Base(root), Dir: true})
			}
			return map[string]any{"entries": out}, nil
		}
		if dir == "" {
			pts, err := src.Points(ctx)
			if err != nil {
				return nil, badRequest(err.Error())
			}
			for _, p := range pts {
				if p.ID == q.Get("point") {
					out := []protect.Entry{}
					for _, root := range p.Roots {
						out = append(out, protect.Entry{Path: root, Name: path.Base(root), Dir: true})
					}
					return map[string]any{"entries": out}, nil
				}
			}
			return nil, notFound("version not found")
		}
		if _, err := cleanAbs(dir); err != nil {
			return nil, err
		}
		list, _, err := src.List(ctx, q.Get("point"), dir, false)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		if list == nil {
			list = []protect.Entry{}
		}
		slices.SortFunc(list, func(a, b protect.Entry) int {
			if a.Dir != b.Dir {
				if a.Dir {
					return -1
				}
				return 1
			}
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
		return map[string]any{"entries": list}, nil
	})

	s.admin("POST /api/restore/preview", func(r *http.Request) (any, error) {
		var req restoreRequest
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		p, err := s.planRestore(ctx, req)
		if err != nil {
			return nil, err
		}
		listing, truncated, err := p.listing(ctx)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		pv := restore.Preview(p.items, listing, p.dest)
		pv.Truncated = truncated
		pv.Target = p.target
		return pv, nil
	})

	s.admin("POST /api/restore/run", func(r *http.Request) (any, error) {
		var req restoreRequest
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		switch req.Policy {
		case restore.PolicyOverwrite, restore.PolicySkip, restore.PolicyRename:
		default:
			return nil, badRequest("choose what to do with existing files")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		p, err := s.planRestore(ctx, req)
		cancel()
		if err != nil {
			return nil, err
		}
		info := p.src.Info()
		what := p.items[0].Path
		if len(p.items) > 1 {
			what = fmt.Sprintf("%d items", len(p.items))
		}
		where := "in place"
		if p.target != "" {
			where = "to " + p.target
		}
		s.audit(r, "backup", fmt.Sprintf("Restore of %s from %s (%s) %s, existing files: %s", what, info.Name, req.Point, where, req.Policy))
		return s.jobs.Start("restore", "Restore from "+info.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			first := p.dest(p.items[0].Path)
			if p.target != "" {
				first = p.target
			}
			if req.Mode == "inplace" && req.Policy == restore.PolicyOverwrite {
				safetySnapshot(first, j.Logf)
			}
			// Backups and clouds are first extracted next to the destination.
			vol := storage.MountOf(first)
			if vol == "" || !strings.HasPrefix(vol, storage.MountRoot+"/") {
				return fmt.Errorf("%s is not on a volume", first)
			}
			scratch, err := os.MkdirTemp(vol, ".strata-restore-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(scratch)
			prog := restore.Progress{Set: func(v float64) { j.SetProgress(v * 0.5) }, Log: j.Logf}
			loc, err := p.src.Stage(ctx, p.point, p.items, scratch, prog)
			if err != nil {
				return err
			}
			res, err := restore.Apply(ctx, loc, restore.Plan{Items: p.items, DstDir: storage.MountRoot, Dest: p.dest, Policy: req.Policy},
				restore.Progress{Set: func(v float64) { j.SetProgress(50 + v/2) }, Log: j.Logf})
			if err != nil {
				return err
			}
			for _, e := range res.First {
				j.Logf("error: %s", e)
			}
			msg := res.Summary()
			j.Logf("%s", msg)
			s.store.Log("info", "backup", userOf(r), "Restore from "+info.Name+": "+msg)
			if res.Errors > 0 {
				return fmt.Errorf("%d file(s) could not be restored", res.Errors)
			}
			return nil
		}), nil
	})

	// A file of a point, or a folder as a zip.
	s.adminRaw("GET /api/restore/download", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		src, err := s.restoreSource(q.Get("source"))
		if err != nil {
			writeErr(w, err)
			return
		}
		abs, err := cleanAbs(q.Get("path"))
		if err != nil {
			writeErr(w, err)
			return
		}
		e, err := src.Stat(r.Context(), q.Get("point"), abs)
		if err != nil {
			writeErr(w, notFound("not in this version"))
			return
		}
		if e.Dir && !src.Info().DownloadDirs {
			writeErr(w, badRequest("folders of a cloud copy cannot be downloaded: restore them to a folder"))
			return
		}
		name := path.Base(abs)
		ctype := "application/octet-stream"
		if e.Dir {
			name += ".zip"
			ctype = "application/zip"
		} else {
			w.Header().Set("Content-Length", fmt.Sprint(e.Size))
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		s.audit(r, "backup", "Downloaded "+abs+" from "+src.Info().Name+" ("+q.Get("point")+")")
		if err := src.Dump(r.Context(), q.Get("point"), abs, w); err != nil {
			// Headers are gone: the browser sees a cut download.
			s.store.Log("warning", "backup", userOf(r), "Download of "+abs+" failed: "+err.Error())
		}
	})

	// A configuration export found among restored files, for the import.
	s.admin("POST /api/restore/config", func(r *http.Request) (any, error) {
		var req struct {
			Path string `json:"path"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		p, err := shares.ValidatePath(req.Path)
		if err != nil || !configFileRe.MatchString(path.Base(p)) {
			return nil, badRequest("not a configuration export")
		}
		rt, err := os.OpenRoot(storage.MountRoot)
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		f, err := rt.Open(strings.TrimPrefix(p, storage.MountRoot+"/"))
		if err != nil {
			return nil, notFound("file not found: restore it first")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 32<<20))
		if err != nil {
			return nil, err
		}
		doc, err := confbackup.Parse(data)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return json.RawMessage(mustJSON(doc)), nil
	})

	s.admin("GET /api/protection", func(r *http.Request) (any, error) {
		now := time.Now()
		rule := protect.Assess(s.protectionCopies(now))
		var pr store.Protection
		tests := []map[string]any{}
		s.store.Read(func(st *store.State) {
			pr = st.Protection
			for _, b := range st.Backups {
				if b.RestoreTest != nil {
					tests = append(tests, map[string]any{"id": "backup:" + b.ID, "name": b.Name, "result": b.RestoreTest})
				}
			}
			for _, c := range st.Clouds {
				if c.RestoreTest != nil {
					tests = append(tests, map[string]any{"id": "cloud:" + c.ID, "name": c.Name, "result": c.RestoreTest})
				}
			}
		})
		ids, _ := s.testable()
		if ids == nil {
			ids = []string{} // the page reads its length
		}
		usb := []map[string]string{}
		s.store.Read(func(st *store.State) {
			for _, v := range st.Volumes {
				if storage.IsMountpoint(v.MountPoint) && storage.OnUSB(v.MountPoint) {
					usb = append(usb, map[string]string{"name": v.Name, "path": v.MountPoint})
				}
			}
		})
		return map[string]any{
			"rule": rule, "restoreTestOff": pr.RestoreTestOff, "lastRestoreTest": pr.LastRestoreTest,
			"testable": ids, "tests": tests, "usbVolumes": usb,
		}, nil
	})

	s.admin("PUT /api/protection", func(r *http.Request) (any, error) {
		var req struct {
			RestoreTestOff bool `json:"restoreTestOff"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		err := s.store.Update(func(st *store.State) error {
			st.Protection.RestoreTestOff = req.RestoreTestOff
			return nil
		})
		if err == nil {
			s.audit(r, "backup", fmt.Sprintf("Monthly restore tests: off=%v", req.RestoreTestOff))
		}
		return nil, err
	})

	s.admin("POST /api/protection/restore-test", func(r *http.Request) (any, error) {
		var req struct {
			ID string `json:"id"`
		}
		_ = decode(r, &req)
		return s.runRestoreTests(userOf(r), req.ID)
	})

	// The snapshots a snapshot task made, and those its retention keeps.
	s.admin("GET /api/schedules/{id}/snapshots", func(r *http.Request) (any, error) {
		var sc *store.Schedule
		s.store.Read(func(st *store.State) {
			if x := st.Schedule(r.PathValue("id")); x != nil {
				c := *x
				sc = &c
			}
		})
		if sc == nil || sc.Type != "snapshot" {
			return nil, notFound("snapshot task not found")
		}
		list, err := scheduler.TaskSnapshots(*sc, s.resolveVolume)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return list, nil
	})
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
