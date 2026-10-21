package api

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"strataos/internal/iscsi"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// iscsiMu serialises every change of the kernel target configuration.
var iscsiMu sync.Mutex

func cloneTarget(t *store.ISCSITarget) *store.ISCSITarget {
	c := *t
	c.Initiators = slices.Clone(t.Initiators)
	c.LUNs = nil
	for _, l := range t.LUNs {
		x := *l
		c.LUNs = append(c.LUNs, &x)
	}
	return &c
}

func (s *Server) iscsiState() (enabled bool, targets []*store.ISCSITarget) {
	s.store.Read(func(st *store.State) {
		enabled = st.ISCSI.Enabled
		for _, t := range st.ISCSI.Targets {
			targets = append(targets, cloneTarget(t))
		}
	})
	return
}

func (s *Server) iscsiTarget(id string) (*store.ISCSITarget, error) {
	_, targets := s.iscsiState()
	for _, t := range targets {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, notFound("iSCSI target not found")
}

// iscsiPorts are the TCP ports the iSCSI portals listen on (none when the service is off).
func (s *Server) iscsiPorts() []int {
	enabled, targets := s.iscsiState()
	ports := []int{}
	if !enabled {
		return ports
	}
	for _, t := range targets {
		if p := cmp.Or(t.Port, iscsi.DefaultPort); !slices.Contains(ports, p) {
			ports = append(ports, p)
		}
	}
	return ports
}

// iscsiApply replaces old by t in the kernel (either may be nil) and saves the
// configuration for the next boot.
func (s *Server) iscsiApply(old, t *store.ISCSITarget) error {
	if old != nil {
		iscsi.Teardown(*old)
	}
	var err error
	if t != nil {
		err = iscsi.Build(*t)
	}
	if serr := iscsi.Save(); serr != nil {
		slog.Warn("iscsi save", "err", serr)
		s.store.Log("warning", "iscsi", "", "iSCSI configuration not saved for the next boot: "+serr.Error())
	}
	return err
}

// iscsiReplace stores t in place of the target with the same ID (or adds it),
// applies it and restores the previous version if the kernel refuses it.
func (s *Server) iscsiReplace(old, t *store.ISCSITarget) error {
	put := func(x *store.ISCSITarget) error {
		return s.store.Update(func(st *store.State) error {
			for i, y := range st.ISCSI.Targets {
				if y.ID == t.ID {
					if x == nil {
						st.ISCSI.Targets = slices.Delete(st.ISCSI.Targets, i, i+1)
					} else {
						st.ISCSI.Targets[i] = cloneTarget(x)
					}
					return nil
				}
			}
			if x != nil {
				st.ISCSI.Targets = append(st.ISCSI.Targets, cloneTarget(x))
			}
			return nil
		})
	}
	if err := put(t); err != nil {
		return err
	}
	enabled, _ := s.iscsiState()
	if !enabled {
		return nil
	}
	if err := s.iscsiApply(old, t); err != nil {
		_ = put(old)
		if old != nil {
			iscsi.Teardown(*t)
			if rerr := s.iscsiApply(nil, old); rerr != nil {
				slog.Warn("iscsi rollback", "err", rerr)
			}
		}
		return &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
	}
	return nil
}

// iscsiConnected refuses an operation on a target with logged-in initiators.
func (s *Server) iscsiConnected(t *store.ISCSITarget, force bool) error {
	if force {
		return nil
	}
	if enabled, _ := s.iscsiState(); !enabled {
		return nil
	}
	if ss := iscsi.Sessions(t.IQN); len(ss) > 0 {
		var names []string
		for _, x := range ss {
			names = append(names, x.Initiator)
		}
		return &Error{Status: http.StatusConflict, Msg: fmt.Sprintf("initiators are connected to %s (%s): disconnect them first, or force", t.Name, strings.Join(names, ", "))}
	}
	return nil
}

// iscsiUsing names the target whose LUN matches, "" if none.
func (s *Server) iscsiUsing(match func(l store.ISCSILUN) bool) string {
	_, targets := s.iscsiState()
	for _, t := range targets {
		for _, l := range t.LUNs {
			if match(*l) {
				return t.Name
			}
		}
	}
	return ""
}

// iscsiGuard refuses to delete storage that backs a LUN.
func (s *Server) iscsiGuard(match func(l store.ISCSILUN) bool) error {
	if t := s.iscsiUsing(match); t != "" {
		return badRequest("this storage is exported over iSCSI by the target " + t + ": remove the LUN first")
	}
	return nil
}

// ---- Backing stores ----

func blockSize(dev string) int64 {
	real, err := filepath.EvalSymlinks(dev)
	if err != nil {
		return 0
	}
	b, err := os.ReadFile("/sys/class/block/" + filepath.Base(real) + "/size")
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n * 512
}

// iscsiBlockBusy tells why a block device cannot be exported ("" = it can).
func (s *Server) iscsiBlockBusy(dev string) string {
	if err := storage.ValidateDevice(dev); err != nil {
		return err.Error()
	}
	if why := storage.BlockInUse(dev); why != "" {
		return why
	}
	real, _ := filepath.EvalSymlinks(dev)
	_, uuid, _ := storage.Probe(real)
	why := ""
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			if uuid != "" && v.UUID == uuid {
				why = "it holds the volume " + v.Name
			}
			for _, d := range v.Devices {
				if r, err := filepath.EvalSymlinks(d); err == nil && r == real {
					why = "it holds the volume " + v.Name
				}
			}
		}
	})
	return why
}

// mountOf returns the mount point holding p (by path, the longest match).
func mountOf(p string) string {
	data, _ := os.ReadFile("/proc/mounts")
	best := ""
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		mp := strings.ReplaceAll(f[1], `\040`, " ")
		if (p == mp || strings.HasPrefix(p, strings.TrimSuffix(mp, "/")+"/")) && len(mp) > len(best) {
			best = mp
		}
	}
	return best
}

// noSymlinks checks that the deepest existing ancestor of p is reached without symbolic links.
func noSymlinks(p string) error {
	for d := p; d != "/" && d != "."; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err != nil {
			continue
		}
		if r, err := filepath.EvalSymlinks(d); err != nil || r != d {
			return fmt.Errorf("%s goes through a symbolic link", p)
		}
		return nil
	}
	return nil
}

// iscsiFile checks (and creates when missing) the image file of a fileio LUN.
func (s *Server) iscsiFile(l *store.ISCSILUN) (created bool, err error) {
	p := l.Path
	if mp := mountOf(p); !strings.HasPrefix(mp, storage.MountRoot+"/") {
		return false, fmt.Errorf("%s is not on a mounted volume", p)
	}
	var share string
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			if p == sh.Path || strings.HasPrefix(p, strings.TrimSuffix(sh.Path, "/")+"/") {
				share = sh.Name
			}
		}
	})
	if share != "" {
		// Users of the share could read or change the disk behind the initiator's back.
		return false, fmt.Errorf("the image file cannot be inside the shared folder %s", share)
	}
	dir := filepath.Dir(p)
	if err := noSymlinks(dir); err != nil {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	if err := noSymlinks(dir); err != nil {
		return false, err
	}
	if fi, err := os.Lstat(p); err == nil {
		if !fi.Mode().IsRegular() {
			return false, fmt.Errorf("%s is not a regular file", p)
		}
		if fi.Size() < 1<<20 || fi.Size()%512 != 0 {
			return false, fmt.Errorf("%s cannot be used: its size is not a multiple of 512 bytes", p)
		}
		l.Size = fi.Size()
		return false, nil
	}
	if l.Size < 1<<20 {
		return false, fmt.Errorf("choose the size of the new image file")
	}
	l.Size = (l.Size + 1<<20 - 1) &^ (1<<20 - 1)
	if u, err := storage.StatFS(mountOf(p)); err == nil && l.Size > u.Total {
		return false, fmt.Errorf("the image file cannot be larger than the volume")
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return false, err
	}
	// Sparse: space is used as the initiator writes.
	err = f.Truncate(l.Size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(p)
		return false, err
	}
	return true, nil
}

type iscsiLUNReq struct {
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	WriteCache bool   `json:"writeCache"`
	ReadOnly   bool   `json:"readOnly"`
	Create     bool   `json:"create"` // lvm / zvol: create the volume (fileio files are created when missing)
	Sparse     bool   `json:"sparse"` // new zvol without reservation
}

func waitDevice(dev string) bool {
	_, _ = sys.Run("udevadm", "settle", "--timeout=10")
	for range 20 {
		if _, err := os.Stat(dev); err == nil {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// iscsiPrepareLUN checks a LUN request and creates its backing store when asked.
// undo removes what it created.
func (s *Server) iscsiPrepareLUN(req iscsiLUNReq, n int) (*store.ISCSILUN, func(), error) {
	l := &store.ISCSILUN{LUN: n, Kind: req.Kind, Path: strings.TrimSpace(req.Path), Size: req.Size, WriteCache: req.WriteCache, ReadOnly: req.ReadOnly, Serial: iscsi.NewSerial()}
	undo := func() {}
	probe := *l
	if probe.Size < 1<<20 {
		probe.Size = 1 << 20 // the size of an existing file is read below
	}
	probe.Size &^= 511
	if err := iscsi.ValidLUN(&probe); err != nil {
		return nil, undo, badRequest(err.Error())
	}
	l.WriteCache = probe.WriteCache
	if other := s.iscsiUsing(func(x store.ISCSILUN) bool { return x.Kind == l.Kind && x.Path == l.Path }); other != "" {
		return nil, undo, badRequest(fmt.Sprintf("%s is already exported by %s", l.Path, other))
	}
	switch l.Kind {
	case "fileio":
		created, err := s.iscsiFile(l)
		if err != nil {
			return nil, undo, badRequest(err.Error())
		}
		if created {
			l.Created = true
			p := l.Path
			undo = func() { _ = os.Remove(p) }
		}
		return l, undo, nil
	case "zvol":
		if !storage.ZFSAvailable() {
			return nil, undo, badRequest("ZFS is not available")
		}
		if req.Create {
			if req.Size < 1<<20 {
				return nil, undo, badRequest("choose the size of the new ZFS volume")
			}
			pool, _, _ := strings.Cut(l.Path, "/")
			if pools, _ := storage.ListZPools(false); !slices.ContainsFunc(pools, func(p storage.ZPool) bool { return p.Name == pool }) {
				return nil, undo, badRequest("unknown ZFS pool " + pool)
			}
			if _, err := sys.Run("zfs", "list", "-H", "-o", "name", l.Path); err == nil {
				return nil, undo, badRequest(l.Path + " already exists")
			}
			size := (req.Size + 1<<20 - 1) &^ (1<<20 - 1)
			if err := storage.CreateDataset(storage.CreateDatasetReq{Name: l.Path, Volume: true, VolSize: size, Sparse: req.Sparse}); err != nil {
				return nil, undo, err
			}
			l.Created = true
			p := l.Path
			undo = func() { _ = storage.DestroyDataset(p, false) }
		} else if out, err := sys.Run("zfs", "get", "-H", "-o", "value", "type", l.Path); err != nil || strings.TrimSpace(out) != "volume" {
			return nil, undo, badRequest(l.Path + " is not a ZFS volume")
		}
	case "lvm":
		vg, lv, _ := strings.Cut(l.Path, "/")
		if req.Create {
			if req.Size < 1<<20 {
				return nil, undo, badRequest("choose the size of the new logical volume")
			}
			if err := storage.CreateLV(vg, storage.CreateLVReq{Name: lv, Size: req.Size}); err != nil {
				return nil, undo, err
			}
			l.Created = true
			undo = func() { _ = storage.RemoveLV(vg, lv) }
		} else if _, err := os.Stat(iscsi.Device(*l)); err != nil {
			return nil, undo, badRequest("logical volume " + l.Path + " not found")
		}
	}
	dev := iscsi.Device(*l)
	if !waitDevice(dev) {
		undo()
		return nil, func() {}, badRequest(dev + " did not appear")
	}
	if why := s.iscsiBlockBusy(dev); why != "" {
		undo()
		return nil, func() {}, badRequest(fmt.Sprintf("%s cannot be exported: %s", dev, why))
	}
	l.Size = blockSize(dev)
	return l, undo, nil
}

// iscsiDeleteBacking removes the file, zvol or logical volume behind a LUN.
func iscsiDeleteBacking(l store.ISCSILUN) error {
	switch l.Kind {
	case "fileio":
		fi, err := os.Lstat(l.Path)
		if err != nil {
			return nil
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", l.Path)
		}
		return os.Remove(l.Path)
	case "zvol":
		return storage.DestroyDataset(l.Path, true) // with its snapshots
	case "lvm":
		vg, lv, _ := strings.Cut(l.Path, "/")
		return storage.RemoveLV(vg, lv)
	}
	return nil
}

// ---- Views ----

type iscsiLUNView struct {
	store.ISCSILUN
	Device    string `json:"device"`
	Snapshots bool   `json:"snapshots"` // the backing store can be snapshotted
	Missing   bool   `json:"missing,omitempty"`
}

type iscsiTargetView struct {
	store.ISCSITarget
	LUNs     []iscsiLUNView  `json:"luns"`
	Portal   string          `json:"portal"`
	Live     bool            `json:"live"`
	Sessions []iscsi.Session `json:"sessions"`
}

func iscsiView(t *store.ISCSITarget, live bool) iscsiTargetView {
	v := iscsiTargetView{ISCSITarget: *t, Portal: iscsi.Portal(*t), LUNs: []iscsiLUNView{}, Sessions: []iscsi.Session{}}
	if v.CHAP.Secret != "" {
		v.CHAP.Secret = masked
	}
	if v.CHAP.MutualSecret != "" {
		v.CHAP.MutualSecret = masked
	}
	if v.Initiators == nil {
		v.Initiators = []string{}
	}
	for _, l := range t.LUNs {
		lv := iscsiLUNView{ISCSILUN: *l, Device: iscsi.Device(*l), Snapshots: l.Kind != "fileio"}
		if _, err := os.Stat(lv.Device); err != nil {
			lv.Missing = true
		}
		v.LUNs = append(v.LUNs, lv)
	}
	if live {
		v.Live = iscsi.Live(*t)
		v.Sessions = iscsi.Sessions(t.IQN)
	}
	return v
}

type iscsiTargetReq struct {
	Name       string          `json:"name"`
	IQN        string          `json:"iqn"`
	PortalIP   string          `json:"portalIp"`
	Port       int             `json:"port"`
	AllowAny   bool            `json:"allowAny"`
	Initiators []string        `json:"initiators"`
	CHAP       store.ISCSICHAP `json:"chap"`
	LUNs       []iscsiLUNReq   `json:"luns"`
}

func iscsiHost() string {
	h, _ := hostname()
	return h
}

// ---- Boot ----

// iscsiLoop checks, after boot, that the kernel runs every target (restored by
// rtslib-fb-targetctl) and rebuilds the missing ones; zvols and LVs may appear late.
func (s *Server) iscsiLoop() {
	var errs []string
	for _, d := range []time.Duration{20 * time.Second, time.Minute, 3 * time.Minute, 10 * time.Minute} {
		time.Sleep(d)
		if errs = s.iscsiReconcile(); len(errs) == 0 {
			return
		}
	}
	for _, e := range errs {
		s.store.Log("error", "iscsi", "", e)
	}
	s.notify("error", "iSCSI target unavailable", strings.Join(errs, "\n"))
}

func (s *Server) iscsiReconcile() []string {
	if !iscsi.Installed() {
		return nil
	}
	iscsiMu.Lock()
	defer iscsiMu.Unlock()
	enabled, targets := s.iscsiState()
	if len(targets) == 0 {
		return nil
	}
	var errs []string
	changed := false
	for _, t := range targets {
		if !enabled {
			if _, err := os.Stat(filepath.Join(iscsi.ConfigFS, "iscsi", t.IQN)); err == nil {
				iscsi.Teardown(*t)
				changed = true
			}
			continue
		}
		if iscsi.Live(*t) {
			continue
		}
		changed = true
		if err := iscsi.Build(*t); err != nil {
			errs = append(errs, fmt.Sprintf("iSCSI target %s could not be started: %v", t.Name, err))
		}
	}
	if changed {
		if err := iscsi.Save(); err != nil {
			slog.Warn("iscsi save", "err", err)
		}
	}
	return errs
}

// ---- Routes ----

func (s *Server) iscsiRoutes() {
	s.admin("GET /api/iscsi", func(r *http.Request) (any, error) {
		enabled, targets := s.iscsiState()
		installed := iscsi.Installed()
		views := []iscsiTargetView{}
		for _, t := range targets {
			views = append(views, iscsiView(t, installed && enabled))
		}
		return map[string]any{"installed": installed, "supported": iscsi.Supported(), "enabled": enabled, "prefix": iscsi.Prefix(iscsiHost()), "ports": s.iscsiPorts(), "targets": views}, nil
	})

	// What a LUN can be backed by.
	s.admin("GET /api/iscsi/backings", func(r *http.Request) (any, error) {
		type loc struct {
			Path  string `json:"path"`
			Label string `json:"label"`
			Free  int64  `json:"free"`
		}
		type blk struct {
			Path   string `json:"path"`
			Size   int64  `json:"size"`
			Busy   string `json:"busy,omitempty"`
			Target string `json:"target,omitempty"`
		}
		out := struct {
			Volumes []loc `json:"volumes"`
			LVs     []blk `json:"lvs"`
			VGs     []loc `json:"vgs"`
			Zvols   []blk `json:"zvols"`
			Pools   []loc `json:"pools"`
			ZFS     bool  `json:"zfs"`
		}{Volumes: []loc{}, LVs: []blk{}, VGs: []loc{}, Zvols: []blk{}, Pools: []loc{}}
		var vols []store.Volume
		s.store.Read(func(st *store.State) {
			for _, v := range st.Volumes {
				vols = append(vols, *v)
			}
		})
		for _, v := range vols {
			if storage.IsMountpoint(v.MountPoint) {
				u, _ := storage.StatFS(v.MountPoint)
				out.Volumes = append(out.Volumes, loc{Path: v.MountPoint, Label: v.Name, Free: u.Free})
			}
		}
		used := func(kind, p string) string {
			return s.iscsiUsing(func(l store.ISCSILUN) bool { return l.Kind == kind && l.Path == p })
		}
		if vgs, err := storage.ListLVM(); err == nil {
			for _, vg := range vgs {
				out.VGs = append(out.VGs, loc{Path: vg.Name, Label: vg.Name, Free: vg.Free})
				for _, lv := range vg.LVs {
					if lv.Path == "" || strings.Contains(lv.Type, "pool") {
						continue
					}
					b := blk{Path: vg.Name + "/" + lv.Name, Size: lv.Size, Target: used("lvm", vg.Name+"/"+lv.Name)}
					if b.Target == "" {
						b.Busy = s.iscsiBlockBusy(lv.Path)
					}
					out.LVs = append(out.LVs, b)
				}
			}
		}
		if storage.ZFSAvailable() {
			out.ZFS = true
			pools, _ := storage.ListZPools(false)
			for _, p := range pools {
				out.Pools = append(out.Pools, loc{Path: p.Name, Label: p.Name, Free: p.Free})
				if u, err := storage.StatFS(storage.MountRoot + "/" + p.Name); err == nil && storage.IsMountpoint(storage.MountRoot+"/"+p.Name) {
					out.Volumes = append(out.Volumes, loc{Path: storage.MountRoot + "/" + p.Name, Label: p.Name + " (zfs)", Free: u.Free})
				}
			}
			if zv, err := sys.Run("zfs", "list", "-Hp", "-t", "volume", "-o", "name,volsize"); err == nil {
				for _, l := range strings.Split(strings.TrimSpace(zv), "\n") {
					f := strings.Split(l, "\t")
					if len(f) < 2 {
						continue
					}
					n, _ := strconv.ParseInt(f[1], 10, 64)
					b := blk{Path: f[0], Size: n, Target: used("zvol", f[0])}
					if b.Target == "" {
						b.Busy = s.iscsiBlockBusy("/dev/zvol/" + f[0])
					}
					out.Zvols = append(out.Zvols, b)
				}
			}
		}
		return out, nil
	})

	s.admin("PUT /api/iscsi/settings", func(r *http.Request) (any, error) {
		var req struct {
			Enabled bool `json:"enabled"`
			Force   bool `json:"force"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		iscsiMu.Lock()
		defer iscsiMu.Unlock()
		_, targets := s.iscsiState()
		if req.Enabled {
			if !iscsi.Installed() {
				return nil, badRequest("install the iSCSI target component first")
			}
			if err := iscsi.Load(); err != nil {
				return nil, badRequest(err.Error())
			}
			_, _ = sys.Run("systemctl", "enable", iscsi.Service)
		} else {
			for _, t := range targets {
				if err := s.iscsiConnected(t, req.Force); err != nil {
					return nil, err
				}
			}
		}
		if err := s.store.Update(func(st *store.State) error { st.ISCSI.Enabled = req.Enabled; return nil }); err != nil {
			return nil, err
		}
		var errs []string
		if iscsi.Installed() {
			for _, t := range targets {
				iscsi.Teardown(*t)
				if req.Enabled {
					if err := iscsi.Build(*t); err != nil {
						errs = append(errs, err.Error())
					}
				}
			}
			if err := iscsi.Save(); err != nil {
				errs = append(errs, "iSCSI configuration not saved for the next boot: "+err.Error())
			}
		}
		if req.Enabled {
			s.audit(r, "iscsi", "iSCSI service enabled")
		} else {
			s.audit(r, "iscsi", "iSCSI service disabled")
		}
		if len(errs) > 0 {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: strings.Join(errs, "; ")}
		}
		return nil, nil
	})

	s.admin("POST /api/iscsi/targets", func(r *http.Request) (any, error) {
		var req iscsiTargetReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if !iscsi.Installed() {
			return nil, badRequest("install the iSCSI target component first")
		}
		iscsiMu.Lock()
		defer iscsiMu.Unlock()
		name := iscsi.Normalize(req.Name)
		t := &store.ISCSITarget{ID: token()[:12], Name: name, IQN: req.IQN, PortalIP: req.PortalIP, Port: req.Port, AllowAny: req.AllowAny, Initiators: req.Initiators, CHAP: req.CHAP, CreatedAt: time.Now()}
		if strings.TrimSpace(t.IQN) == "" {
			t.IQN = iscsi.GenerateIQN(iscsiHost(), name)
		}
		_, others := s.iscsiState()
		if err := iscsi.Validate(t, others); err != nil {
			return nil, badRequest(err.Error())
		}
		if len(req.LUNs) == 0 {
			return nil, badRequest("add at least one LUN")
		}
		var undos []func()
		rollback := func() {
			for i := len(undos) - 1; i >= 0; i-- {
				undos[i]()
			}
		}
		for i, lr := range req.LUNs {
			l, undo, err := s.iscsiPrepareLUN(lr, i)
			if err != nil {
				rollback()
				return nil, err
			}
			undos = append(undos, undo)
			t.LUNs = append(t.LUNs, l)
		}
		if err := iscsi.Validate(t, others); err != nil {
			rollback()
			return nil, badRequest(err.Error())
		}
		if err := s.iscsiReplace(nil, t); err != nil {
			rollback()
			return nil, err
		}
		s.audit(r, "iscsi", "Created iSCSI target "+t.Name)
		return iscsiView(t, false), nil
	})

	s.admin("PUT /api/iscsi/targets/{id}", func(r *http.Request) (any, error) {
		var req iscsiTargetReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		iscsiMu.Lock()
		defer iscsiMu.Unlock()
		old, err := s.iscsiTarget(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		t := cloneTarget(old)
		t.IQN, t.PortalIP, t.Port, t.AllowAny, t.Initiators, t.CHAP = req.IQN, req.PortalIP, req.Port, req.AllowAny, req.Initiators, req.CHAP
		if strings.TrimSpace(t.IQN) == "" {
			t.IQN = iscsi.GenerateIQN(iscsiHost(), t.Name)
		}
		if t.CHAP.Secret == masked {
			t.CHAP.Secret = old.CHAP.Secret
		}
		if t.CHAP.MutualSecret == masked {
			t.CHAP.MutualSecret = old.CHAP.MutualSecret
		}
		_, others := s.iscsiState()
		if err := iscsi.Validate(t, others); err != nil {
			return nil, badRequest(err.Error())
		}
		if err := s.iscsiReplace(old, t); err != nil {
			return nil, err
		}
		s.audit(r, "iscsi", "Changed iSCSI target "+t.Name)
		return nil, nil
	})

	s.admin("DELETE /api/iscsi/targets/{id}", func(r *http.Request) (any, error) {
		iscsiMu.Lock()
		defer iscsiMu.Unlock()
		t, err := s.iscsiTarget(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		if err := s.iscsiConnected(t, r.URL.Query().Get("force") == "1"); err != nil {
			return nil, err
		}
		if iscsi.Installed() {
			_ = s.iscsiApply(t, nil)
		}
		if err := s.store.Update(func(st *store.State) error {
			st.ISCSI.Targets = slices.DeleteFunc(st.ISCSI.Targets, func(x *store.ISCSITarget) bool { return x.ID == t.ID })
			return nil
		}); err != nil {
			return nil, err
		}
		s.audit(r, "iscsi", "Deleted iSCSI target "+t.Name)
		if r.URL.Query().Get("backing") == "1" {
			var errs []error
			for _, l := range t.LUNs {
				errs = append(errs, iscsiDeleteBacking(*l))
			}
			if err := errors.Join(errs...); err != nil {
				return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: "target deleted, but not all its storage: " + err.Error()}
			}
		}
		return nil, nil
	})

	s.admin("POST /api/iscsi/targets/{id}/luns", func(r *http.Request) (any, error) {
		var req iscsiLUNReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		iscsiMu.Lock()
		defer iscsiMu.Unlock()
		old, err := s.iscsiTarget(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		n := 0
		for slices.ContainsFunc(old.LUNs, func(l *store.ISCSILUN) bool { return l.LUN == n }) {
			n++
		}
		l, undo, err := s.iscsiPrepareLUN(req, n)
		if err != nil {
			return nil, err
		}
		t := cloneTarget(old)
		t.LUNs = append(t.LUNs, l)
		_, others := s.iscsiState()
		if err := iscsi.Validate(t, others); err != nil {
			undo()
			return nil, badRequest(err.Error())
		}
		if err := s.iscsiReplace(old, t); err != nil {
			undo()
			return nil, err
		}
		s.audit(r, "iscsi", fmt.Sprintf("Added LUN %d to iSCSI target %s", n, t.Name))
		return nil, nil
	})

	lunOf := func(r *http.Request) (*store.ISCSITarget, *store.ISCSITarget, *store.ISCSILUN, error) {
		old, err := s.iscsiTarget(r.PathValue("id"))
		if err != nil {
			return nil, nil, nil, err
		}
		n, err := strconv.Atoi(r.PathValue("lun"))
		if err != nil {
			return nil, nil, nil, badRequest("invalid LUN")
		}
		t := cloneTarget(old)
		for _, l := range t.LUNs {
			if l.LUN == n {
				return old, t, l, nil
			}
		}
		return nil, nil, nil, notFound("LUN not found")
	}

	s.admin("PUT /api/iscsi/targets/{id}/luns/{lun}", func(r *http.Request) (any, error) {
		var req struct {
			WriteCache bool `json:"writeCache"`
			ReadOnly   bool `json:"readOnly"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		iscsiMu.Lock()
		defer iscsiMu.Unlock()
		old, t, l, err := lunOf(r)
		if err != nil {
			return nil, err
		}
		l.WriteCache, l.ReadOnly = req.WriteCache, req.ReadOnly
		_, others := s.iscsiState()
		if err := iscsi.Validate(t, others); err != nil {
			return nil, badRequest(err.Error())
		}
		if err := s.iscsiReplace(old, t); err != nil {
			return nil, err
		}
		s.audit(r, "iscsi", fmt.Sprintf("Changed LUN %d of iSCSI target %s", l.LUN, t.Name))
		return nil, nil
	})

	s.admin("DELETE /api/iscsi/targets/{id}/luns/{lun}", func(r *http.Request) (any, error) {
		iscsiMu.Lock()
		defer iscsiMu.Unlock()
		old, t, l, err := lunOf(r)
		if err != nil {
			return nil, err
		}
		if err := s.iscsiConnected(old, r.URL.Query().Get("force") == "1"); err != nil {
			return nil, err
		}
		t.LUNs = slices.DeleteFunc(t.LUNs, func(x *store.ISCSILUN) bool { return x.LUN == l.LUN })
		if err := s.iscsiReplace(old, t); err != nil {
			return nil, err
		}
		s.audit(r, "iscsi", fmt.Sprintf("Removed LUN %d from iSCSI target %s", l.LUN, t.Name))
		if r.URL.Query().Get("backing") == "1" {
			if err := iscsiDeleteBacking(*l); err != nil {
				return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: "LUN removed, but not its storage: " + err.Error()}
			}
		}
		return nil, nil
	})

	// Snapshots come from the backing store (ZFS or LVM), crash-consistent.
	s.admin("POST /api/iscsi/targets/{id}/luns/{lun}/snapshot", func(r *http.Request) (any, error) {
		_, t, l, err := lunOf(r)
		if err != nil {
			return nil, err
		}
		stamp := time.Now().Format("060102-150405")
		var name string
		switch l.Kind {
		case "zvol":
			name = "iscsi-" + stamp
			err = storage.Snapshot(l.Path, name, false)
			name = l.Path + "@" + name
		case "lvm":
			vg, lv, _ := strings.Cut(l.Path, "/")
			name = lv[:min(len(lv), 14)] + "-" + stamp
			// Room for the changes made while the snapshot lives: a tenth of the volume.
			size := max(int64(1<<30), blockSize(iscsi.Device(*l))/10)
			err = storage.SnapshotLV(vg, lv, name, size)
			name = vg + "/" + name
		default:
			return nil, badRequest("snapshots need a LUN on a ZFS volume or an LVM logical volume")
		}
		if err != nil {
			return nil, err
		}
		s.audit(r, "iscsi", fmt.Sprintf("Snapshot %s of LUN %d of iSCSI target %s", name, l.LUN, t.Name))
		return map[string]string{"snapshot": name}, nil
	})
}
