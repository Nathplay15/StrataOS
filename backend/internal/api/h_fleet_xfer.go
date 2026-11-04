package api

// Copying or moving a VM or a container between two NAS of the Multi-NAS
// console, outside a cluster. The console drives; each NAS works on its side
// through the same operations, called directly when it is the console itself
// and over the fleet link (/api/fleet/peer/xfer/*, needs the "vms"
// permission) otherwise:
//
//   - source: "export" stops the VM (or takes a temporary snapshot for a
//     live copy), writes the VM backup format (vms.ExportVM) or a container
//     archive (lxc.Export) into <volume>/.strata-fleet/<id>/data and the
//     SHA-256 of every file, as a job;
//   - the console copies the files in 8 MB gzip chunks, from the offset the
//     destination already has (a failed transfer resumes where it stopped);
//   - destination: "receive" checks the name, folder, bridges and free space
//     and starts a job that waits for the data; "commit" verifies the
//     checksums and restores (vms.RestoreVM with the bridges mapped, or
//     lxc.Import), then starts it if asked;
//   - "finish" deletes the source for a move, "abort" puts things back.
//
// Each side keeps its state in xfer.json next to the files, so a restart of
// a NAS does not lose a transfer that can resume.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/lxc"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/vms"
)

// xferSide is the state of a transfer on one NAS.
type xferSide struct {
	xferReq
	Role       string    `json:"role"`  // source | dest
	Owner      string    `json:"owner"` // FleetManager.ID of the console, or "local"
	Dir        string    `json:"dir"`
	Stage      string    `json:"stage"` // source: exporting | ready | done; dest: receiving | committing | restoring | done; failed
	WasRunning bool      `json:"wasRunning,omitempty"`
	Error      string    `json:"error,omitempty"`
	Job        string    `json:"job,omitempty"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
}

type xferEntry struct {
	mu      sync.Mutex
	st      xferSide
	seen    time.Time // last call of the console
	running bool      // a job works on it
}

var xferSides = struct {
	sync.Mutex
	m map[string]*xferEntry
}{m: map[string]*xferEntry{}}

const xferDirName = ".strata-fleet"

func (e *xferEntry) get() xferSide {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.st
	st.Files = slices.Clone(st.Files)
	return st
}

// update changes the state and saves it.
func (e *xferEntry) update(fn func(st *xferSide)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(&e.st)
	e.saveLocked()
}

func (e *xferEntry) saveLocked() {
	e.st.Updated = time.Now()
	b, _ := json.MarshalIndent(e.st, "", "  ")
	if err := os.WriteFile(filepath.Join(e.st.Dir, "xfer.json"), b, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("transfer state not saved", "id", e.st.ID, "err", err)
	}
}

func (e *xferEntry) fail(err error) {
	e.update(func(st *xferSide) { st.Stage, st.Error = "failed", err.Error() })
}

// xferLookup finds a transfer, in memory or (after a restart) on a volume.
func xferLookup(id string) *xferEntry {
	if !validXferID(id) {
		return nil
	}
	xferSides.Lock()
	defer xferSides.Unlock()
	if e := xferSides.m[id]; e != nil {
		return e
	}
	found, _ := filepath.Glob(filepath.Join(storage.MountRoot, "*", xferDirName, id, "xfer.json"))
	for _, f := range found {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var st xferSide
		if json.Unmarshal(b, &st) != nil || st.ID != id || st.Dir != filepath.Dir(f) {
			continue
		}
		if st.Stage == "exporting" || st.Stage == "receiving" || st.Stage == "committing" || st.Stage == "restoring" {
			st.Stage, st.Error = "failed", "interrupted by a restart of the NAS"
		}
		e := &xferEntry{st: st, seen: time.Now()}
		xferSides.m[id] = e
		return e
	}
	return nil
}

// xferOwned returns a transfer of this console.
func xferOwned(id, owner string) (*xferEntry, error) {
	e := xferLookup(id)
	if e == nil {
		return nil, notFound("transfer not found")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.st.Owner != owner {
		return nil, notFound("transfer not found")
	}
	e.seen = time.Now()
	return e, nil
}

func xferForget(id string) {
	xferSides.Lock()
	delete(xferSides.m, id)
	xferSides.Unlock()
}

// stagingDir is the transfer folder on the volume holding p.
func stagingDir(p, id string) (string, error) {
	c := filepath.Clean(p)
	if !strings.HasPrefix(c, storage.MountRoot+"/") {
		return "", fmt.Errorf("%s is not inside a volume", p)
	}
	root := vms.VolumeRoot(c)
	if !storage.IsMountpoint(root) {
		return "", fmt.Errorf("the volume %s is not mounted", root)
	}
	return filepath.Join(root, xferDirName, id), nil
}

// noSpace turns "no space left" into a clear message.
func noSpace(err error, dir string) error {
	if err != nil && strings.Contains(err.Error(), "No space left") {
		return fmt.Errorf("not enough free space on %s: %v", vms.VolumeRoot(dir), err)
	}
	return err
}

// ---- Inventory ----

type xferObj struct {
	Name     string   `json:"name"`
	State    string   `json:"state"`
	Bridges  []string `json:"bridges"`
	Ceph     bool     `json:"ceph,omitempty"`
	Template bool     `json:"template,omitempty"`
	Location string   `json:"location,omitempty"`
	Size     int64    `json:"size,omitempty"` // VM: virtual size of the disks
}

type xferVolume struct {
	Path string `json:"path"`
	Free int64  `json:"free"`
}

type xferInventory struct {
	Name       string       `json:"name"`
	Libvirt    bool         `json:"libvirt"`
	LXC        bool         `json:"lxc"`
	VMs        []xferObj    `json:"vms"`
	Containers []xferObj    `json:"containers"`
	Bridges    []string     `json:"bridges"`
	Volumes    []xferVolume `json:"volumes"`
}

func (s *Server) xferInventory() xferInventory {
	inv := xferInventory{Name: sysHostname(), Libvirt: vms.Installed(), LXC: lxc.Installed(), VMs: []xferObj{}, Containers: []xferObj{}, Bridges: hostBridges(), Volumes: []xferVolume{}}
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			if v.MountPoint != "" && storage.IsMountpoint(v.MountPoint) {
				inv.Volumes = append(inv.Volumes, xferVolume{Path: v.MountPoint})
			}
		}
	})
	for i := range inv.Volumes {
		if u, err := storage.StatFS(inv.Volumes[i].Path); err == nil {
			inv.Volumes[i].Free = int64(u.Free)
		}
	}
	if inv.Libvirt {
		list, _ := vms.List()
		for _, vm := range list {
			o := xferObj{Name: vm.Name, State: vm.State, Bridges: []string{}, Template: vm.Template, Location: vm.Dir}
			for _, d := range vm.Disks {
				if d.Device != "disk" {
					continue
				}
				if strings.HasPrefix(d.Source, "rbd:") {
					o.Ceph = true
				}
				o.Size += d.Size
			}
			for _, n := range vm.NICs {
				if n.Type == "bridge" && !slices.Contains(o.Bridges, n.Source) {
					o.Bridges = append(o.Bridges, n.Source)
				}
			}
			inv.VMs = append(inv.VMs, o)
		}
	}
	if inv.LXC {
		for _, ct := range lxc.List() {
			o := xferObj{Name: ct.Name, State: ct.State, Bridges: []string{}, Location: ct.Storage.Location}
			if ct.Network.Mode == "bridge" && ct.Network.Bridge != "" {
				o.Bridges = append(o.Bridges, ct.Network.Bridge)
			}
			inv.Containers = append(inv.Containers, o)
		}
	}
	return inv
}

// ---- Operations of a side ----

// xferView is what a side tells the console.
type xferView struct {
	Stage string         `json:"stage"`
	Error string         `json:"error,omitempty"`
	Files []xferFile     `json:"files"`
	Job   *jobs.Snapshot `json:"job,omitempty"`
}

func (s *Server) xferViewOf(e *xferEntry) xferView {
	st := e.get()
	v := xferView{Stage: st.Stage, Error: st.Error, Files: st.Files}
	if v.Files == nil {
		v.Files = []xferFile{}
	}
	if st.Role == "dest" {
		for i := range v.Files {
			v.Files[i].Have = 0
			if fi, err := os.Stat(filepath.Join(st.Dir, "data", v.Files[i].Name)); err == nil {
				v.Files[i].Have = fi.Size()
			}
		}
	}
	if j, ok := s.jobs.Get(st.Job); ok {
		if len(j.Log) > 4096 {
			j.Log = j.Log[len(j.Log)-4096:]
		}
		v.Job = &j
	}
	return v
}

// xferOp runs an operation of the transfer protocol for a console (owner:
// FleetManager.ID, or "local" for this NAS's own console).
func (s *Server) xferOp(owner, console, op string, q xferReq) (any, error) {
	if op == "inventory" {
		return s.xferInventory(), nil
	}
	if !validXferID(q.ID) {
		return nil, badRequest("invalid transfer ID")
	}
	switch op {
	case "export":
		return s.xferExportStart(owner, console, q)
	case "receive":
		return s.xferReceiveStart(owner, console, q)
	}
	e, err := xferOwned(q.ID, owner)
	if err != nil {
		return nil, err
	}
	switch op {
	case "state":
		return s.xferViewOf(e), nil
	case "commit":
		st := e.get()
		if st.Role != "dest" {
			return nil, badRequest("not a destination")
		}
		if st.Stage == "done" || st.Stage == "committing" || st.Stage == "restoring" {
			return s.xferViewOf(e), nil
		}
		e.mu.Lock()
		running := e.running
		e.mu.Unlock()
		if st.Stage != "receiving" || !running {
			return nil, &Error{Status: http.StatusConflict, Msg: "the destination is not receiving: resume the transfer"}
		}
		e.update(func(st *xferSide) { st.Stage = "committing" })
		return s.xferViewOf(e), nil
	case "finish":
		return nil, s.xferFinish(e)
	case "abort":
		s.xferAbort(e)
		return nil, nil
	}
	return nil, notFound("unknown operation")
}

// startSideJob runs fn as this NAS's job of the transfer.
func (s *Server) startSideJob(e *xferEntry, title, user string, fn func(ctx context.Context, j *jobs.Job) error) {
	e.mu.Lock()
	e.running = true
	e.mu.Unlock()
	snap := s.jobs.Start("fleet", title, user, func(ctx context.Context, j *jobs.Job) error {
		defer func() {
			e.mu.Lock()
			e.running = false
			e.mu.Unlock()
		}()
		err := fn(ctx, j)
		if err != nil {
			e.fail(err)
		}
		return err
	})
	e.update(func(st *xferSide) { st.Job = snap.ID })
}

func kindLabel(kind string) string {
	if kind == "vm" {
		return "VM"
	}
	return "container"
}

func consoleUser(console string) string {
	if console == "" {
		return "console"
	}
	return console + " (console)"
}

// xferExportStart prepares the copy of a VM or container on its NAS (or
// returns the copy already prepared: a resume).
func (s *Server) xferExportStart(owner, console string, q xferReq) (any, error) {
	if err := checkXferReq(&q); err != nil {
		return nil, err
	}
	if e := xferLookup(q.ID); e != nil {
		st := e.get()
		if st.Owner != owner || st.Role != "source" {
			return nil, notFound("transfer not found")
		}
		e.mu.Lock()
		e.seen = time.Now()
		running := e.running
		e.mu.Unlock()
		if st.Stage == "ready" || running {
			return s.xferViewOf(e), nil
		}
		// Failed export: start again.
		s.startExport(e, console)
		return s.xferViewOf(e), nil
	}
	var where string
	switch q.Kind {
	case "vm":
		if err := vms.ValidName(q.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		vm, err := vms.Get(q.Name)
		if err != nil {
			return nil, notFound("VM not found")
		}
		where = vm.Dir
		for _, d := range vm.Disks {
			if d.Device != "disk" {
				continue
			}
			if !strings.HasPrefix(d.Source, storage.MountRoot+"/") {
				return nil, badRequest("only VMs whose disks are files on a volume can be copied to another NAS")
			}
			if where == "" {
				where = d.Source
			}
		}
	case "lxc":
		if err := lxc.ValidName(q.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		spec, err := lxc.Load(q.Name)
		if err != nil {
			return nil, notFound("container not found")
		}
		where = spec.Storage.Location
	}
	dir, err := stagingDir(where, q.ID)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(dir), 0o700)
	e := &xferEntry{st: xferSide{xferReq: q, Role: "source", Owner: owner, Dir: dir, Stage: "exporting", Created: time.Now()}, seen: time.Now()}
	e.st.Files = nil
	e.update(func(*xferSide) {})
	xferSides.Lock()
	xferSides.m[q.ID] = e
	xferSides.Unlock()
	s.store.Log("info", "fleet", consoleUser(console), fmt.Sprintf("Copy of %s %s to %s started by the console %s", kindLabel(q.Kind), q.Name, q.Peer, firstNonEmpty(console, "this NAS")))
	s.startExport(e, console)
	return s.xferViewOf(e), nil
}

func (s *Server) startExport(e *xferEntry, console string) {
	st := e.get()
	e.update(func(st *xferSide) { st.Stage, st.Error = "exporting", "" })
	title := fmt.Sprintf("Preparing the copy of %s %s for %s", kindLabel(st.Kind), st.Name, st.Peer)
	s.startSideJob(e, title, consoleUser(console), func(ctx context.Context, j *jobs.Job) error {
		return s.xferExport(ctx, e, j)
	})
}

// xferExport writes the copy and its checksums.
func (s *Server) xferExport(ctx context.Context, e *xferEntry, j *jobs.Job) error {
	st := e.get()
	data := filepath.Join(st.Dir, "data")
	_ = os.RemoveAll(data)
	if err := os.MkdirAll(data, 0o700); err != nil {
		return err
	}
	p := vms.Progress{Set: func(v float64) { j.SetProgress(v * 0.9) }, Log: j.Logf}
	switch st.Kind {
	case "vm":
		state := vms.State(st.Name)
		mode := ""
		running := state == "running" || state == "paused"
		if running && st.Mode == "stop" {
			j.Logf("Shutting %s down", st.Name)
			if err := vms.Action(st.Name, "shutdown"); err != nil {
				return err
			}
			if !waitVMState(ctx, st.Name, "shut off", 5*time.Minute) {
				return fmt.Errorf("%s did not shut down within 5 minutes: shut it down from its console, then resume", st.Name)
			}
			e.update(func(x *xferSide) { x.WasRunning = true })
		} else if running {
			mode = vms.BackupSnapshot
		}
		_, err := vms.ExportVM(ctx, st.Name, data, mode, p)
		if running && st.Mode == "stop" && (!st.Move || err != nil) {
			j.Logf("Starting %s again", st.Name)
			if serr := vms.Action(st.Name, "start"); serr != nil {
				j.Logf("%s did not start again: %v", st.Name, serr)
			}
		}
		if err != nil {
			return noSpace(err, st.Dir)
		}
	case "lxc":
		mode := "stop"
		if st.Mode == "snapshot" {
			mode = ""
		}
		if st.Move && lxc.State(st.Name) == "running" {
			j.Logf("Stopping %s", st.Name)
			if err := lxc.Stop(st.Name); err != nil || !lxc.WaitState(ctx, st.Name, "stopped", 2*time.Minute) {
				_ = lxc.Kill(st.Name)
				lxc.WaitState(ctx, st.Name, "stopped", 30*time.Second)
			}
			e.update(func(x *xferSide) { x.WasRunning = true })
		}
		j.SetProgress(-1)
		if _, _, err := lxc.Export(ctx, st.Name, lxc.ExportOpts{Dir: data, File: st.Name + ".tar.zst", Mode: mode, AnyDir: true}, j.Logf); err != nil {
			if st.Move && e.get().WasRunning {
				_ = lxc.Start(context.Background(), st.Name)
			}
			return noSpace(err, st.Dir)
		}
	}
	files, err := xferHashDir(ctx, data, func(f float64) { j.SetProgress(90 + 10*f) })
	if err != nil {
		return err
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	j.Logf("Copy ready: %d file(s), %d MB", len(files), total>>20)
	e.update(func(x *xferSide) { x.Files, x.Stage = files, "ready" })
	return nil
}

func waitVMState(ctx context.Context, name, want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if vms.State(name) == want {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
	}
	return false
}

// xferHashDir lists the files of a copy with their SHA-256.
func xferHashDir(ctx context.Context, dir string, progress func(float64)) ([]xferFile, error) {
	var files []xferFile
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if !validXferFile(rel) {
			return fmt.Errorf("unexpected file %s in the copy", rel)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, xferFile{Name: rel, Size: fi.Size()})
		total += fi.Size()
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 || len(files) > 64 {
		return nil, fmt.Errorf("unexpected copy (%d files)", len(files))
	}
	var done int64
	for i := range files {
		sum, err := xferSHA256(ctx, filepath.Join(dir, files[i].Name), func(n int64) {
			if total > 0 {
				progress(float64(done+n) / float64(total))
			}
		})
		if err != nil {
			return nil, err
		}
		files[i].SHA256 = sum
		done += files[i].Size
	}
	return files, nil
}

func xferSHA256(ctx context.Context, path string, progress func(int64)) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	var n int64
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		k, err := f.Read(buf)
		h.Write(buf[:k])
		n += int64(k)
		if n%(64<<20) < int64(k) {
			progress(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// xferCheckDest checks a destination: installed, free name, folder in a
// volume, bridges present. Returns the staging folder.
func (s *Server) xferCheckDest(q xferReq) (string, error) {
	switch q.Kind {
	case "vm":
		if !vms.Installed() {
			return "", badRequest("virtual machines are not installed on this NAS")
		}
		if err := vms.ValidName(q.NewName); err != nil {
			return "", badRequest(err.Error())
		}
		if _, err := vms.Get(q.NewName); err == nil {
			return "", badRequest(fmt.Sprintf("a VM named %s already exists on this NAS", q.NewName))
		}
	case "lxc":
		if !lxc.Installed() {
			return "", badRequest("containers (LXC) are not installed on this NAS")
		}
		if err := lxc.ValidName(q.NewName); err != nil {
			return "", badRequest(err.Error())
		}
		if lxc.Exists(q.NewName) {
			return "", badRequest(fmt.Sprintf("a container named %s already exists on this NAS", q.NewName))
		}
	}
	bridges := hostBridges()
	for _, to := range q.Bridges {
		if to != "nat" && !slices.Contains(bridges, to) {
			return "", badRequest(fmt.Sprintf("the bridge %s does not exist on this NAS", to))
		}
	}
	dir, err := stagingDir(q.Storage, q.ID)
	if err != nil {
		return "", badRequest(err.Error())
	}
	return dir, nil
}

// xferReceiveStart prepares this NAS to receive a copy (or resumes).
func (s *Server) xferReceiveStart(owner, console string, q xferReq) (any, error) {
	if err := checkXferReq(&q); err != nil {
		return nil, err
	}
	if len(q.Files) == 0 {
		return nil, badRequest("invalid file list")
	}
	dir, err := s.xferCheckDest(q)
	e := xferLookup(q.ID)
	if e != nil {
		st := e.get()
		if st.Owner != owner || st.Role != "dest" {
			return nil, notFound("transfer not found")
		}
		e.mu.Lock()
		e.seen = time.Now()
		running := e.running
		e.mu.Unlock()
		if running || st.Stage == "done" {
			return s.xferViewOf(e), nil
		}
		if err != nil {
			return nil, err
		}
		// Resume: same files, options of the new request.
		if !sameFiles(st.Files, q.Files) {
			return nil, badRequest("the copy changed at the source: discard this transfer and start again")
		}
		e.update(func(x *xferSide) {
			x.Storage, x.Bridges, x.Start, x.NewName, x.Peer = q.Storage, q.Bridges, q.Start, q.NewName, q.Peer
		})
	} else {
		if err != nil {
			return nil, err
		}
		if u, err := storage.StatFS(vms.VolumeRoot(dir)); err == nil {
			if need := xferNeed(q.Kind, q.Files); int64(u.Free) < need {
				return nil, badRequest(fmt.Sprintf("not enough free space on %s: %d MB needed, %d MB free", vms.VolumeRoot(dir), need>>20, u.Free>>20))
			}
		}
		if err := os.MkdirAll(filepath.Join(dir, "data"), 0o700); err != nil {
			return nil, err
		}
		_ = os.Chmod(filepath.Dir(dir), 0o700)
		e = &xferEntry{st: xferSide{xferReq: q, Role: "dest", Owner: owner, Dir: dir, Created: time.Now()}, seen: time.Now()}
		for _, f := range q.Files {
			if strings.Contains(f.Name, "/") {
				_ = os.MkdirAll(filepath.Join(dir, "data", filepath.Dir(f.Name)), 0o700)
			}
		}
		xferSides.Lock()
		xferSides.m[q.ID] = e
		xferSides.Unlock()
		s.store.Log("info", "fleet", consoleUser(console), fmt.Sprintf("Receiving %s %s from %s, started by the console %s", kindLabel(q.Kind), q.Name, q.Peer, firstNonEmpty(console, "this NAS")))
	}
	e.update(func(x *xferSide) { x.Stage, x.Error = "receiving", "" })
	title := fmt.Sprintf("Receiving %s %s from %s", kindLabel(q.Kind), q.NewName, q.Peer)
	s.startSideJob(e, title, consoleUser(console), func(ctx context.Context, j *jobs.Job) error {
		return s.xferReceive(ctx, e, j)
	})
	return s.xferViewOf(e), nil
}

func sameFiles(a, b []xferFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Size != b[i].Size || a[i].SHA256 != b[i].SHA256 {
			return false
		}
	}
	return true
}

// xferReceive waits for the data, then verifies and restores.
func (s *Server) xferReceive(ctx context.Context, e *xferEntry, j *jobs.Job) error {
	data := filepath.Join(e.get().Dir, "data")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		st := e.get()
		v := s.xferViewOf(e)
		total, have := xferTotal(v.Files)
		if total > 0 {
			j.SetProgress(80 * float64(have) / float64(total))
		}
		if st.Stage == "committing" {
			break
		}
		e.mu.Lock()
		idle := time.Since(e.seen)
		e.mu.Unlock()
		if idle > xferIdle {
			return errors.New("no data from the console for 30 minutes: resume the transfer from the console")
		}
	}
	e.update(func(x *xferSide) { x.Stage = "restoring" })
	st := e.get()
	j.Logf("Verifying the checksums")
	for _, f := range st.Files {
		p := filepath.Join(data, f.Name)
		fi, err := os.Stat(p)
		if err != nil || fi.Size() != f.Size {
			return fmt.Errorf("%s is incomplete: resume the transfer", f.Name)
		}
		sum, err := xferSHA256(ctx, p, func(int64) {})
		if err != nil {
			return err
		}
		if sum != f.SHA256 {
			_ = os.Remove(p)
			return fmt.Errorf("checksum mismatch on %s: it is sent again when you resume the transfer", f.Name)
		}
	}
	j.SetProgress(85)
	p := vms.Progress{Set: func(v float64) { j.SetProgress(85 + v*0.15) }, Log: j.Logf}
	switch st.Kind {
	case "vm":
		err := vms.RestoreVM(ctx, data, vms.RestoreReq{Name: st.NewName, Storage: st.Storage, Start: st.Start,
			NewIdentity: !st.Move, Bridges: st.Bridges, MoveFiles: true}, p)
		if err != nil {
			return noSpace(err, st.Dir)
		}
	case "lxc":
		archive := filepath.Join(data, st.Name+".tar.zst")
		spec, err := lxc.ReadArchiveSpec(ctx, archive)
		if err != nil {
			return err
		}
		backend := spec.Storage.Backend
		if _, err := lxc.CheckLocation(st.Storage, backend); err != nil {
			j.Logf("The volume %s has another file system: the container is stored as a folder", st.Storage)
			backend = "dir"
		}
		bridge := ""
		if spec.Network.Mode == "bridge" {
			bridge = st.Bridges[spec.Network.Bridge]
		}
		req := lxc.ImportReq{Source: archive, Name: st.NewName, Location: st.Storage, Backend: backend, Start: st.Start, Bridge: bridge, NewMAC: !st.Move}
		if err := lxc.Import(ctx, req, j.Logf); err != nil {
			return noSpace(err, st.Dir)
		}
	}
	_ = os.RemoveAll(data)
	e.update(func(x *xferSide) { x.Stage = "done" })
	j.SetProgress(100)
	return nil
}

// xferFinish ends a transfer that succeeded: the source of a move is deleted.
func (s *Server) xferFinish(e *xferEntry) error {
	st := e.get()
	if st.Role == "source" && st.Stage != "ready" && st.Stage != "done" {
		return &Error{Status: http.StatusConflict, Msg: "the copy is not ready"}
	}
	if st.Role == "dest" && st.Stage != "done" {
		return &Error{Status: http.StatusConflict, Msg: "the copy is not restored"}
	}
	if st.Role == "source" && st.Stage == "ready" {
		if st.Move {
			switch st.Kind {
			case "vm":
				if err := vms.Delete(st.Name, true); err != nil {
					s.store.Log("warning", "fleet", "", fmt.Sprintf("VM %s moved to %s, but it could not be deleted here: %v", st.Name, st.Peer, err))
				} else {
					_ = s.setVMAutostart(st.Name, false, 0, 0)
				}
				s.store.Log("info", "vms", "", fmt.Sprintf("VM %s moved to %s", st.Name, st.Peer))
			case "lxc":
				if err := lxc.Delete(context.Background(), st.Name); err != nil {
					s.store.Log("warning", "fleet", "", fmt.Sprintf("Container %s moved to %s, but it could not be deleted here: %v", st.Name, st.Peer, err))
				}
				s.dropContainerFromBackups(st.Name)
				s.store.Log("info", "vms", "", fmt.Sprintf("Container %s moved to %s", st.Name, st.Peer))
			}
		} else {
			s.store.Log("info", "vms", "", fmt.Sprintf("%s %s copied to %s", map[string]string{"vm": "VM", "lxc": "Container"}[st.Kind], st.Name, st.Peer))
		}
		e.update(func(x *xferSide) { x.Stage = "done" })
	}
	if st.Role == "dest" {
		s.store.Log("info", "vms", "", fmt.Sprintf("%s %s received from %s", map[string]string{"vm": "VM", "lxc": "Container"}[st.Kind], st.NewName, st.Peer))
	}
	_ = os.RemoveAll(st.Dir)
	return nil
}

// xferAbort stops a transfer and puts things back: the source of a move
// runs again, the files of the copy are removed.
func (s *Server) xferAbort(e *xferEntry) {
	st := e.get()
	if j, ok := s.jobs.Get(st.Job); ok && j.Status == jobs.Running {
		s.jobs.Cancel(st.Job)
		for range 60 {
			if j, ok := s.jobs.Get(st.Job); !ok || j.Status != jobs.Running {
				break
			}
			time.Sleep(time.Second)
		}
	}
	if st.Role == "source" && st.Move && st.WasRunning && st.Stage != "done" {
		switch st.Kind {
		case "vm":
			if vms.State(st.Name) == "shut off" {
				_ = vms.Action(st.Name, "start")
			}
		case "lxc":
			if lxc.Exists(st.Name) && lxc.State(st.Name) == "stopped" {
				_ = lxc.Start(context.Background(), st.Name)
			}
		}
	}
	if st.Stage != "done" {
		s.store.Log("info", "fleet", "", fmt.Sprintf("Transfer of %s %s cancelled", kindLabel(st.Kind), st.Name))
	}
	_ = os.RemoveAll(st.Dir)
	xferForget(st.ID)
}

// xferRead returns a gzip chunk of a file of the copy, and its raw length.
func (s *Server) xferRead(e *xferEntry, file string, off int64) ([]byte, int, error) {
	st := e.get()
	if st.Role != "source" || st.Stage != "ready" {
		return nil, 0, &Error{Status: http.StatusConflict, Msg: "the copy is not ready"}
	}
	i := slices.IndexFunc(st.Files, func(f xferFile) bool { return f.Name == file })
	if i < 0 {
		return nil, 0, notFound("file not found")
	}
	if off < 0 || off >= st.Files[i].Size {
		return nil, 0, badRequest("invalid offset")
	}
	f, err := os.Open(filepath.Join(st.Dir, "data", file))
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	buf := make([]byte, min(int64(xferChunk), st.Files[i].Size-off))
	n, err := f.ReadAt(buf, off)
	if err != nil && !(err == io.EOF && n == len(buf)) {
		return nil, 0, err
	}
	return gzipChunk(buf[:n]), n, nil
}

// xferWrite appends a gzip chunk to a file of the copy at off, which must be
// what this side already has (the console asks again on a conflict).
func (s *Server) xferWrite(e *xferEntry, file string, off int64, gz []byte) (int64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.st
	if st.Role != "dest" || st.Stage != "receiving" || !e.running {
		return 0, &Error{Status: http.StatusConflict, Msg: "the destination is not receiving: resume the transfer"}
	}
	i := slices.IndexFunc(st.Files, func(f xferFile) bool { return f.Name == file })
	if i < 0 {
		return 0, notFound("file not found")
	}
	p := filepath.Join(st.Dir, "data", file)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if fi.Size() != off {
		return fi.Size(), &Error{Status: http.StatusConflict, Msg: "offset mismatch: " + strconv.FormatInt(fi.Size(), 10)}
	}
	b, err := gunzipChunk(gz, xferChunk)
	if err != nil {
		return off, badRequest("invalid chunk: " + err.Error())
	}
	if off+int64(len(b)) > st.Files[i].Size {
		return off, badRequest("more data than the file holds")
	}
	if _, err := f.WriteAt(b, off); err != nil {
		return off, noSpace(err, st.Dir)
	}
	e.seen = time.Now()
	return off + int64(len(b)), nil
}

// xferJanitor marks the transfers a restart interrupted and removes the
// files of transfers abandoned for xferKeep.
func (s *Server) xferJanitor() {
	_ = s.store.Update(func(st *store.State) error {
		for i := range st.Fleet.Transfers {
			if t := &st.Fleet.Transfers[i]; t.Status == "running" {
				t.Status, t.Error = "failed", "interrupted by a restart of this NAS: resume it"
			}
		}
		return nil
	})
	for {
		found, _ := filepath.Glob(filepath.Join(storage.MountRoot, "*", xferDirName, "*", "xfer.json"))
		for _, f := range found {
			fi, err := os.Stat(f)
			if err != nil || time.Since(fi.ModTime()) < xferKeep {
				continue
			}
			id := filepath.Base(filepath.Dir(f))
			xferSides.Lock()
			e := xferSides.m[id]
			xferSides.Unlock()
			if e != nil {
				e.mu.Lock()
				busy := e.running || time.Since(e.seen) < xferKeep
				e.mu.Unlock()
				if busy {
					continue
				}
			}
			_ = os.RemoveAll(filepath.Dir(f))
			xferForget(id)
			s.store.Log("info", "fleet", "", "Files of an abandoned transfer removed: "+filepath.Dir(f))
		}
		time.Sleep(6 * time.Hour)
	}
}

// ---- Peer routes ----

func (s *Server) fleetXferPeerRoutes() {
	s.fleetPeer("POST /api/fleet/peer/xfer/{op}", "vms", false, func(r *http.Request, m store.FleetManager) (any, error) {
		var q xferReq
		if err := decode(r, &q); err != nil {
			return nil, err
		}
		return s.xferOp(m.ID, m.Name, r.PathValue("op"), q)
	})
	chunkArgs := func(r *http.Request) (string, int64, error) {
		file := r.URL.Query().Get("file")
		off, err := strconv.ParseInt(r.URL.Query().Get("off"), 10, 64)
		if !validXferFile(file) || err != nil || off < 0 {
			return "", 0, badRequest("invalid chunk")
		}
		return file, off, nil
	}
	s.mux.HandleFunc("GET /api/fleet/peer/xfer-chunk/{id}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := s.fleetPeerAuth(w, r, "vms", "xfer", xferChunksPerMin)
		if !ok {
			return
		}
		e, err := xferOwned(r.PathValue("id"), m.ID)
		if err != nil {
			writeErr(w, err)
			return
		}
		file, off, err := chunkArgs(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		gz, n, err := s.xferRead(e, file, off)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("X-Strata-Len", strconv.Itoa(n))
		_, _ = w.Write(gz)
	})
	s.mux.HandleFunc("PUT /api/fleet/peer/xfer-chunk/{id}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := s.fleetPeerAuth(w, r, "vms", "xfer", xferChunksPerMin)
		if !ok {
			return
		}
		e, err := xferOwned(r.PathValue("id"), m.ID)
		if err != nil {
			writeErr(w, err)
			return
		}
		file, off, err := chunkArgs(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		gz, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*xferChunk))
		if err != nil {
			writeErr(w, badRequest("chunk too large"))
			return
		}
		have, err := s.xferWrite(e, file, off, gz)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"have": have})
	})
}
