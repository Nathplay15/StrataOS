package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/storage"
	"strataos/internal/storagetools"
	"strataos/internal/store"
)

// Space analyzer, duplicate finder and disk burn-in test. The first two read
// every file of a volume: they are reserved to administrators.

var tools = struct {
	mu      sync.Mutex
	scans   map[string]string // analyzed path → running job id
	dupJob  string
	burnIns map[string]*storagetools.BurnIn // device → running test
}{scans: map[string]string{}, burnIns: map[string]*storagetools.BurnIn{}}

func (s *Server) toolsDir() string {
	d := filepath.Join(s.dataDir, "storagetools")
	_ = os.MkdirAll(d, 0o700)
	return d
}

type scanTarget struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	Kind  string `json:"kind"` // volume | dataset | share
}

// scanTargets lists what the analyzer can scan: mounted volumes, ZFS datasets and shares.
func (s *Server) scanTargets() []scanTarget {
	out := []scanTarget{}
	var vols []store.Volume
	var shares []store.Share
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			vols = append(vols, *v)
		}
		for _, sh := range st.Shares {
			shares = append(shares, *sh)
		}
	})
	seen := map[string]bool{}
	add := func(t scanTarget) {
		if !seen[t.Path] {
			seen[t.Path] = true
			out = append(out, t)
		}
	}
	for _, v := range vols {
		if storage.IsMountpoint(v.MountPoint) {
			add(scanTarget{Path: v.MountPoint, Label: v.Name, Kind: "volume"})
		}
	}
	if storage.ZFSAvailable() {
		ds, _ := storage.ListZDatasets("", "filesystem")
		for _, d := range ds {
			if strings.HasPrefix(d.Mountpoint, storage.MountRoot+"/") {
				add(scanTarget{Path: d.Mountpoint, Label: d.Name, Kind: "dataset"})
			}
		}
	}
	for _, sh := range shares {
		if info, err := os.Stat(sh.Path); err == nil && info.IsDir() {
			add(scanTarget{Path: sh.Path, Label: sh.Name, Kind: "share"})
		}
	}
	return out
}

// toolsFolder validates a folder chosen for a scan: inside the NAS storage,
// existing, and reached without any symbolic link.
func toolsFolder(p string) (string, error) {
	clean := filepath.Clean("/" + p)
	if !strings.HasPrefix(clean, storage.MountRoot+"/") {
		return "", badRequest("the folder must be on a NAS volume")
	}
	real, err := filepath.EvalSymlinks(clean)
	if err != nil || real != clean {
		return "", badRequest("folder not found")
	}
	if info, err := os.Stat(clean); err != nil || !info.IsDir() {
		return "", badRequest("folder not found")
	}
	return clean, nil
}

func (s *Server) analyzerFile(p string) string {
	h := sha256.Sum256([]byte(p))
	return filepath.Join(s.toolsDir(), "analyzer-"+hex.EncodeToString(h[:8])+".json")
}

func (s *Server) loadAnalysis(p string) (*storagetools.Report, error) {
	b, err := os.ReadFile(s.analyzerFile(p))
	if err != nil {
		return nil, err
	}
	var r storagetools.Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func writeJSONFile(p string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *Server) dupFile() string { return filepath.Join(s.toolsDir(), "duplicates.json") }

func (s *Server) loadDuplicates() (*storagetools.DupReport, error) {
	b, err := os.ReadFile(s.dupFile())
	if err != nil {
		return nil, err
	}
	var r storagetools.DupReport
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// recycleShare returns the share with a recycle bin holding p ("" when none).
func (s *Server) recycleShare(p string) (sharePath string) {
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			if sh.RecycleBin && strings.HasPrefix(p, sh.Path+"/") && len(sh.Path) > len(sharePath) {
				sharePath = sh.Path
			}
		}
	})
	return
}

// trashFile moves a file into the recycle bin of its share, where Samba and
// WebDAV put deleted files (.recycle/<user>/<path>), or deletes it when the
// share has none. Everything goes through os.Root: links cannot lead outside.
func (s *Server) trashFile(user, p string) (recycled bool, err error) {
	if sp := s.recycleShare(p); sp != "" {
		rt, err := os.OpenRoot(sp)
		if err != nil {
			return false, err
		}
		defer rt.Close()
		rel := relTo(sp, p)
		dst := path.Join(".recycle", user, rel)
		if err := rt.MkdirAll(path.Dir(dst), 0o770); err != nil {
			return false, err
		}
		if _, err := rt.Lstat(dst); err == nil {
			dst += "." + strconv.FormatInt(time.Now().Unix(), 10)
		}
		if err := rt.Rename(rel, dst); err != nil {
			return false, err
		}
		now := time.Now()
		_ = rt.Chtimes(dst, now, now) // the bin is purged by age of deletion
		return true, nil
	}
	rt, err := os.OpenRoot(storage.MountRoot)
	if err != nil {
		return false, err
	}
	defer rt.Close()
	return false, rt.Remove(relTo(storage.MountRoot, p))
}

func (s *Server) storageToolsRoutes() {
	// ---- Space analyzer ----
	s.admin("GET /api/storagetools/targets", func(r *http.Request) (any, error) {
		type view struct {
			scanTarget
			Scanned *time.Time `json:"scanned,omitempty"`
			Size    int64      `json:"size,omitempty"`
			Job     string     `json:"job,omitempty"`
		}
		out := []view{}
		for _, t := range s.scanTargets() {
			v := view{scanTarget: t}
			if info, err := os.Stat(s.analyzerFile(t.Path)); err == nil {
				m := info.ModTime()
				v.Scanned = &m
				if rep, err := s.loadAnalysis(t.Path); err == nil {
					v.Size = rep.Size
				}
			}
			tools.mu.Lock()
			v.Job = tools.scans[t.Path]
			tools.mu.Unlock()
			out = append(out, v)
		}
		return out, nil
	})
	s.admin("GET /api/storagetools/analyzer", func(r *http.Request) (any, error) {
		p := filepath.Clean("/" + r.URL.Query().Get("path"))
		tools.mu.Lock()
		job := tools.scans[p]
		tools.mu.Unlock()
		rep, err := s.loadAnalysis(p)
		if err != nil {
			return map[string]any{"job": job}, nil
		}
		dir := r.URL.Query().Get("dir")
		tree := rep.Subtree(dir, 2, 100)
		if tree == nil {
			return nil, notFound("folder not in the analysis")
		}
		rep.Tree = nil
		return map[string]any{"job": job, "report": rep, "tree": tree}, nil
	})
	s.admin("POST /api/storagetools/analyzer", func(r *http.Request) (any, error) {
		var req struct {
			Path string `json:"path"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		p := filepath.Clean("/" + req.Path)
		known := false
		for _, t := range s.scanTargets() {
			known = known || t.Path == p
		}
		if !known {
			return nil, badRequest("choose a volume, a dataset or a shared folder")
		}
		tools.mu.Lock()
		defer tools.mu.Unlock()
		if id := tools.scans[p]; id != "" {
			return nil, badRequest("an analysis of this folder is already running")
		}
		j := s.jobs.Start("analyzer", "Space analysis of "+p, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			defer func() {
				tools.mu.Lock()
				delete(tools.scans, p)
				tools.mu.Unlock()
			}()
			used := int64(0)
			if u, err := storage.StatFS(p); err == nil {
				used = u.Used
			}
			rep, err := storagetools.Analyze(ctx, p, storagetools.ScanOpts{Progress: func(files, size int64) {
				if used > 0 {
					j.SetProgress(min(99, float64(size)*100/float64(used)))
				}
			}})
			if err != nil {
				return err
			}
			j.Logf("%d files, %d folders, %d MiB on disk (%d unreadable entries) in %s", rep.Files, rep.Dirs, rep.Size>>20, rep.Errors, rep.Finished.Sub(rep.Started).Round(time.Second))
			return writeJSONFile(s.analyzerFile(p), rep)
		})
		tools.scans[p] = j.ID
		return j, nil
	})

	// ---- Duplicates ----
	s.admin("GET /api/storagetools/duplicates", func(r *http.Request) (any, error) {
		tools.mu.Lock()
		job := tools.dupJob
		tools.mu.Unlock()
		bins := []string{}
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				if sh.RecycleBin {
					bins = append(bins, sh.Path)
				}
			}
		})
		out := map[string]any{"job": job, "recycleBins": bins}
		if rep, err := s.loadDuplicates(); err == nil {
			out["report"] = rep
		}
		return out, nil
	})
	s.admin("POST /api/storagetools/duplicates", func(r *http.Request) (any, error) {
		var req struct {
			Paths   []string `json:"paths"`
			MinSize int64    `json:"minSize"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Paths) == 0 || len(req.Paths) > 20 {
			return nil, badRequest("choose 1 to 20 folders")
		}
		var roots []string
		for _, p := range req.Paths {
			c, err := toolsFolder(p)
			if err != nil {
				return nil, err
			}
			roots = append(roots, c)
		}
		if req.MinSize < 0 {
			return nil, badRequest("invalid minimum size")
		}
		if req.MinSize == 0 {
			req.MinSize = storagetools.DefaultMinSize
		}
		tools.mu.Lock()
		defer tools.mu.Unlock()
		if tools.dupJob != "" {
			return nil, badRequest("a duplicate search is already running")
		}
		j := s.jobs.Start("duplicates", "Duplicate search", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			defer func() {
				tools.mu.Lock()
				tools.dupJob = ""
				tools.mu.Unlock()
			}()
			j.Logf("Folders: %s", strings.Join(roots, ", "))
			rep, err := storagetools.FindDuplicates(ctx, roots, req.MinSize, func(stage string, done, total int64) {
				if stage == "compare" && total > 0 {
					j.SetProgress(min(99, float64(done)*100/float64(total)))
				}
			})
			if err != nil {
				return err
			}
			j.Logf("%d files compared, %d groups of identical files, %d MiB wasted", rep.Scanned, len(rep.Groups), rep.Wasted>>20)
			return writeJSONFile(s.dupFile(), rep)
		})
		tools.dupJob = j.ID
		return j, nil
	})
	s.admin("POST /api/storagetools/duplicates/delete", func(r *http.Request) (any, error) {
		var req struct {
			Paths []string `json:"paths"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Paths) == 0 {
			return nil, badRequest("no file selected")
		}
		tools.mu.Lock()
		defer tools.mu.Unlock()
		if tools.dupJob != "" {
			return nil, badRequest("a duplicate search is running")
		}
		rep, err := s.loadDuplicates()
		if err != nil {
			return nil, notFound("no duplicate search result")
		}
		plan, err := storagetools.PlanDeletion(rep, req.Paths)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		user := userOf(r)
		deleted := map[string]bool{}
		var recycled, removed int
		var freed int64
		skipped := []string{}
		for _, d := range plan {
			if !strings.HasPrefix(d.File.Path, storage.MountRoot+"/") || !storagetools.Unchanged(d.File, d.Size) {
				skipped = append(skipped, d.File.Path)
				continue
			}
			// The last-copy rule is checked on disk too: a kept copy must still be there, unchanged.
			kept := false
			for _, k := range d.Keeps {
				if !deleted[k.Path] && storagetools.Unchanged(k, d.Size) {
					kept = true
					break
				}
			}
			if !kept {
				skipped = append(skipped, d.File.Path)
				continue
			}
			bin, err := s.trashFile(user, d.File.Path)
			if err != nil {
				skipped = append(skipped, d.File.Path)
				continue
			}
			deleted[d.File.Path] = true
			if bin {
				recycled++
			} else {
				removed++
				freed += d.Size
			}
		}
		storagetools.Forget(rep, deleted)
		_ = writeJSONFile(s.dupFile(), rep)
		s.audit(r, "storage", fmt.Sprintf("Duplicates: %d file(s) moved to the recycle bin, %d deleted", recycled, removed))
		return map[string]any{"recycled": recycled, "deleted": removed, "freed": freed, "skipped": skipped}, nil
	})

	// ---- Burn-in test ----
	s.admin("GET /api/storage/burnin", func(r *http.Request) (any, error) {
		running := []storagetools.BurnInStatus{}
		tools.mu.Lock()
		for _, b := range tools.burnIns {
			running = append(running, b.Status())
		}
		tools.mu.Unlock()
		sort.Slice(running, func(i, j int) bool { return running[i].Device < running[j].Device })
		reports := []store.BurnInReport{}
		s.store.Read(func(st *store.State) {
			for i := len(st.BurnIns) - 1; i >= 0; i-- {
				reports = append(reports, *st.BurnIns[i])
			}
		})
		return map[string]any{"running": running, "reports": reports}, nil
	})
	s.admin("DELETE /api/storage/burnin/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		return nil, s.store.Update(func(st *store.State) error {
			for i, b := range st.BurnIns {
				if b.ID == id {
					st.BurnIns = append(st.BurnIns[:i], st.BurnIns[i+1:]...)
					return nil
				}
			}
			return notFound("report not found")
		})
	})
	s.admin("POST /api/storage/burnin", func(r *http.Request) (any, error) {
		var req struct {
			Device    string `json:"device"`
			Mode      string `json:"mode"`    // read | write
			Confirm   string `json:"confirm"` // write mode: the disk name, typed by the user
			ShortTest bool   `json:"shortTest"`
			LongTest  bool   `json:"longTest"`
			// Blocks limits the test to the first blocks of 4 KiB (end-to-end tests; not in the interface).
			Blocks int64 `json:"blocks"`
			// Passes repeats a read-only scan (end-to-end tests: something long enough to cancel).
			Passes int `json:"passes"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Mode != "read" && req.Mode != "write" {
			return nil, badRequest("mode must be read or write")
		}
		// Same rules as creating a volume: a whole, unused disk only.
		if err := storage.ValidateFreeDisks([]string{req.Device}, false); err != nil {
			return nil, badRequest(err.Error())
		}
		disks, err := storage.ListDisks()
		if err != nil {
			return nil, err
		}
		var disk *storage.BlockDevice
		for i := range disks {
			if disks[i].Path == req.Device {
				disk = &disks[i]
			}
		}
		if disk == nil || disk.System || disk.Usage != "free" {
			return nil, badRequest("only a free whole disk can be tested")
		}
		if req.Mode == "write" && req.Confirm != disk.Name {
			return nil, badRequest("type the disk name to confirm: this test erases the whole disk")
		}
		if req.Passes < 0 || req.Passes > 1000 {
			return nil, badRequest("invalid number of passes")
		}
		if req.Blocks < 0 || req.Blocks*4096 > disk.Size {
			return nil, badRequest("invalid block range")
		}
		if err := storage.SetBusy(disk.Path, "burn-in test"); err != nil {
			return nil, badRequest(err.Error())
		}
		o := storagetools.BurnInOpts{Device: disk.Path, Write: req.Mode == "write", ShortTest: req.ShortTest, LongTest: req.LongTest, Blocks: req.Blocks, Passes: req.Passes, Size: disk.Size}
		rep := &store.BurnInReport{ID: shortID(), Device: disk.Path, Serial: disk.Serial, Model: disk.Model, Size: disk.Size, Mode: req.Mode, Blocks: req.Blocks, User: userOf(r)}
		mode := "non-destructive read test"
		if o.Write {
			mode = "destructive write test (erases the disk)"
		}
		s.audit(r, "storage", fmt.Sprintf("Burn-in test of %s (%s %s): %s", disk.Path, disk.Model, disk.Serial, mode))
		j := s.jobs.Start("burnin", "Burn-in test of "+disk.Path, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			defer storage.ClearBusy(disk.Path)
			defer func() {
				tools.mu.Lock()
				delete(tools.burnIns, disk.Path)
				tools.mu.Unlock()
			}()
			b := storagetools.NewBurnIn(o, disk.Serial, j.ID, j.Logf, j.SetProgress)
			tools.mu.Lock()
			tools.burnIns[disk.Path] = b
			tools.mu.Unlock()
			b.Run(ctx, rep)
			_ = s.store.Update(func(st *store.State) error {
				st.BurnIns = append(st.BurnIns, rep)
				if n := len(st.BurnIns); n > store.MaxBurnIns {
					st.BurnIns = st.BurnIns[n-store.MaxBurnIns:]
				}
				return nil
			})
			name := fmt.Sprintf("%s (%s %s)", disk.Path, disk.Model, disk.Serial)
			switch rep.Result {
			case "passed":
				msg := fmt.Sprintf("Disk %s passed its burn-in test", name)
				s.store.Log("info", "storage", rep.User, msg)
				s.notify("info", "Disk burn-in test passed", msg)
				return nil
			case "cancelled":
				s.store.Log("info", "storage", rep.User, fmt.Sprintf("Burn-in test of %s cancelled", name))
				return storagetools.ErrCancelled
			}
			// The failed job is logged and notified (as "Task failed") with this message.
			return fmt.Errorf("disk %s failed its burn-in test: %s", name, strings.Join(rep.Problems, ", "))
		})
		return j, nil
	})
}
