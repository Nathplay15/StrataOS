package api

// Ceph cluster built by the NAS of the cluster (h_cluster.go): bootstrap,
// nodes, disks (OSDs), pools, maintenance and the dashboard. The NAS where
// the administrator works orchestrates; each node runs its own daemons
// through /api/cluster/peer/ceph/<op> (cephNodeOps).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/ceph"
	"strataos/internal/cluster"
	"strataos/internal/jobs"
	"strataos/internal/storage"
	"strataos/internal/store"
)

func (s *Server) cephLocal() *store.CephLocal {
	var l *store.CephLocal
	s.store.Read(func(st *store.State) {
		if st.Ceph.Local != nil {
			c := *st.Ceph.Local
			c.Mons = slices.Clone(st.Ceph.Local.Mons)
			l = &c
		}
	})
	return l
}

// cephConfOf renders ceph.conf for the local cluster.
func cephConfOf(l store.CephLocal) string {
	mons := make([]ceph.Mon, len(l.Mons))
	for i, m := range l.Mons {
		mons[i] = ceph.Mon{Name: m.Name, Addr: m.Addr}
	}
	return ceph.Config(ceph.ConfParams{FSID: l.FSID, Mons: mons, PublicNet: l.PublicNet, ClusterNet: l.ClusterNet, Size: l.Size, MinSize: l.MinSize, SingleHost: l.SingleHost, SmallMemory: l.SmallMemory})
}

// cephBusy runs one orchestrated Ceph operation at a time on this NAS.
func (s *Server) cephJob(title, user string, fn func(ctx context.Context, j *jobs.Job) error) (jobs.Snapshot, error) {
	if _, busy := running.LoadOrStore("ceph", true); busy {
		return jobs.Snapshot{}, &Error{Status: http.StatusConflict, Msg: "another Ceph operation is running"}
	}
	return s.jobs.Start("ceph", title, user, func(ctx context.Context, j *jobs.Job) error {
		defer running.Delete("ceph")
		return fn(ctx, j)
	}), nil
}

// cephNode runs a node operation on member m: directly on this NAS, through
// its peer API on another one.
func (s *Server) cephNode(ctx context.Context, m store.ClusterMember, op string, in, out any, timeout time.Duration) error {
	c := s.clusterState()
	if m.ID == c.Self {
		h, ok := s.cephNodeOps()[op]
		if !ok {
			return fmt.Errorf("unknown operation %s", op)
		}
		raw, _ := json.Marshal(in)
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		v, err := h(ctx, raw)
		if err != nil || out == nil {
			return err
		}
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	}
	return s.clusterCall(ctx, m, timeout, "ceph/"+op, in, out)
}

// memberInfo gets the information of a member (this NAS included).
func (s *Server) memberInfo(ctx context.Context, m store.ClusterMember) (clusterPeerInfo, error) {
	if m.ID == s.clusterState().Self {
		return s.localClusterInfo(), nil
	}
	var info clusterPeerInfo
	err := s.clusterCall(ctx, m, 10*time.Second, "info", nil, &info)
	return info, err
}

// cephLocalDisks is the disk list of this NAS for OSDs.
func (s *Server) cephLocalDisks() ([]ceph.Disk, error) {
	disks, err := storage.ListDisks()
	if err != nil {
		return nil, err
	}
	used := map[string]string{}
	var vols []store.Volume
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			vols = append(vols, *v)
		}
	})
	for _, v := range vols {
		devs := storage.DevicesByUUID(v.UUID)
		devs = append(devs, v.Devices...)
		for _, d := range devs {
			used[d] = "volume " + v.Name
		}
	}
	return ceph.Eligibility(disks, used, storage.BusyReason), nil
}

// cephNodeOps are the operations a node runs on itself, for the NAS that
// orchestrates (peer API) or for itself. Names come from this NAS (its host
// name), never from the request.
func (s *Server) cephNodeOps() map[string]func(ctx context.Context, raw json.RawMessage) (any, error) {
	name := sysHostname()
	needNode := func() error {
		if s.cephLocal() == nil {
			return badRequest("this NAS is not a Ceph node")
		}
		return nil
	}
	return map[string]func(ctx context.Context, raw json.RawMessage) (any, error){
		"base": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req struct {
				Conf      string          `json:"conf"`
				Admin     string          `json:"admin"`
				Bootstrap string          `json:"bootstrap"`
				Local     store.CephLocal `json:"local"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, badRequest("invalid data")
			}
			if !ceph.Installed() {
				return nil, badRequest("install the Ceph component on " + name + " first")
			}
			if err := ceph.ValidFSID(req.Local.FSID); err != nil || ceph.KeyOf(req.Admin, "client.admin") == "" {
				return nil, badRequest("invalid data")
			}
			if l := s.cephLocal(); l != nil && l.FSID != req.Local.FSID {
				return nil, badRequest("this NAS is already a node of another Ceph cluster")
			}
			if err := ceph.WriteBase(req.Conf, req.Admin, req.Bootstrap); err != nil {
				return nil, err
			}
			l := req.Local
			return nil, s.store.Update(func(st *store.State) error { st.Ceph.Local = &l; return nil })
		},
		"conf": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req struct {
				Local store.CephLocal `json:"local"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, badRequest("invalid data")
			}
			cur := s.cephLocal()
			if cur == nil || cur.FSID != req.Local.FSID {
				return nil, badRequest("this NAS is not a node of this Ceph cluster")
			}
			l := req.Local
			if err := ceph.WriteConf(cephConfOf(l)); err != nil {
				return nil, err
			}
			return nil, s.store.Update(func(st *store.State) error { st.Ceph.Local = &l; return nil })
		},
		"mon": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req struct {
				Addr    string `json:"addr"`
				MonMap  string `json:"monmap"`
				Keyring string `json:"keyring"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, badRequest("invalid data")
			}
			if err := needNode(); err != nil {
				return nil, err
			}
			info := s.localClusterInfo()
			if info.Addr == "" || req.Addr != info.Addr {
				return nil, badRequest("unexpected monitor address")
			}
			mm, err := base64.StdEncoding.DecodeString(req.MonMap)
			if err != nil || len(mm) == 0 || len(mm) > 1<<20 {
				return nil, badRequest("invalid monitor map")
			}
			return nil, ceph.CreateMon(ctx, name, info.Addr, mm, req.Keyring)
		},
		"mgr": func(ctx context.Context, raw json.RawMessage) (any, error) {
			if err := needNode(); err != nil {
				return nil, err
			}
			return nil, ceph.CreateMgr(ctx, name)
		},
		"mds": func(ctx context.Context, raw json.RawMessage) (any, error) {
			if err := needNode(); err != nil {
				return nil, err
			}
			return nil, ceph.CreateMDS(ctx, name)
		},
		"osd": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req struct {
				Device string `json:"device"`
				Erase  bool   `json:"erase"`
				ID     *int   `json:"id"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, badRequest("invalid data")
			}
			if err := needNode(); err != nil {
				return nil, err
			}
			disks, err := s.cephLocalDisks()
			if err != nil {
				return nil, err
			}
			i := slices.IndexFunc(disks, func(d ceph.Disk) bool { return d.Path == req.Device })
			if i < 0 {
				return nil, badRequest("disk " + req.Device + " not found on " + name)
			}
			d := disks[i]
			if !d.Eligible {
				return nil, badRequest(fmt.Sprintf("disk %s of %s cannot be used: %s", d.Path, name, d.Reason))
			}
			if d.Erase && !req.Erase {
				return nil, badRequest(fmt.Sprintf("disk %s of %s holds data: confirm that it may be erased", d.Path, name))
			}
			id := -1
			if req.ID != nil {
				id = *req.ID
			}
			if err := storage.SetBusy(d.Path, "creating a Ceph OSD"); err != nil {
				return nil, badRequest(err.Error())
			}
			defer storage.ClearBusy(d.Path)
			var log strings.Builder
			storage.Lock.Lock()
			err = ceph.CreateOSD(ctx, d.Path, d.Erase, id, func(f string, a ...any) { fmt.Fprintf(&log, f+"\n", a...) })
			storage.Lock.Unlock()
			out := log.String()
			if len(out) > 8000 {
				out = out[len(out)-8000:]
			}
			if err != nil {
				return nil, fmt.Errorf("%v\n%s", err, out)
			}
			return map[string]string{"log": out}, nil
		},
		"osd-stop": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req struct {
				ID  int  `json:"id"`
				Zap bool `json:"zap"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, badRequest("invalid data")
			}
			local, err := ceph.LocalOSDs(ctx)
			if err != nil {
				return nil, err
			}
			if _, ok := local[req.ID]; !ok {
				return nil, badRequest(fmt.Sprintf("osd.%d is not on %s", req.ID, name))
			}
			_ = ceph.StopOSD(req.ID)
			return nil, nil
		},
		"osd-zap": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req struct {
				ID int `json:"id"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, badRequest("invalid data")
			}
			local, err := ceph.LocalOSDs(ctx)
			if err != nil {
				return nil, err
			}
			if _, ok := local[req.ID]; !ok {
				return nil, nil // already gone
			}
			_ = ceph.StopOSD(req.ID)
			storage.Lock.Lock()
			defer storage.Lock.Unlock()
			return nil, ceph.ZapOSD(ctx, req.ID, func(string, ...any) {})
		},
		"daemon-remove": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req struct {
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, badRequest("invalid data")
			}
			return nil, ceph.RemoveDaemon(ctx, req.Kind, name)
		},
		"purge": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var inUse []string
			s.store.Read(func(st *store.State) {
				for _, v := range st.Ceph.Volumes {
					if v.Cluster == ceph.LocalCluster {
						inUse = append(inUse, v.VolumeID)
					}
				}
				for _, p := range st.Ceph.VMPools {
					if p.Cluster == ceph.LocalCluster {
						inUse = append(inUse, p.Name)
					}
				}
			})
			if len(inUse) > 0 {
				return nil, badRequest(name + " still uses storage of this Ceph cluster: remove its Ceph volumes and VM storage first")
			}
			if local, err := ceph.LocalOSDs(ctx); err == nil {
				storage.Lock.Lock()
				for id := range local {
					_ = ceph.StopOSD(id)
					_ = ceph.ZapOSD(ctx, id, func(string, ...any) {})
				}
				storage.Lock.Unlock()
			}
			_ = ceph.Purge()
			return nil, s.store.Update(func(st *store.State) error {
				st.Ceph.Local, st.Ceph.LastHealth = nil, ""
				return nil
			})
		},
		"osds": func(ctx context.Context, raw json.RawMessage) (any, error) {
			local, err := ceph.LocalOSDs(ctx)
			if err != nil {
				return nil, err
			}
			out := map[string][]string{}
			for id, d := range local {
				out[strconv.Itoa(id)] = d
			}
			return out, nil
		},
	}
}

func (s *Server) cephPeerRoutes() {
	s.clusterPeer("POST /api/cluster/peer/ceph/{op}", true, func(r *http.Request) (any, error) {
		h, ok := s.cephNodeOps()[r.PathValue("op")]
		if !ok {
			return nil, notFound("unknown operation")
		}
		var raw json.RawMessage
		if err := decode(r, &raw); err != nil {
			return nil, err
		}
		return h(r.Context(), raw)
	})
}

// cephMembers are the members of the cluster that are Ceph nodes.
func (s *Server) cephMembers() []store.ClusterMember {
	c := s.clusterState()
	l := s.cephLocal()
	var out []store.ClusterMember
	for _, m := range c.Members {
		if len(m.Roles) > 0 || (l != nil && m.ID == c.Self) {
			out = append(out, m)
		}
	}
	return out
}

// cephBootstrapReq creates the Ceph cluster over the members.
type cephBootstrapReq struct {
	Test    bool                `json:"test"`
	Disks   map[string][]string `json:"disks"` // member ID → disk paths
	Erase   bool                `json:"erase"` // erasing disks with data is confirmed
	RBD     bool                `json:"rbd"`
	RBDPool string              `json:"rbdPool"`
	CephFS  bool                `json:"cephfs"`
	FSName  string              `json:"fsName"`
	Size    int                 `json:"size"`
	MinSize int                 `json:"minSize"`
}

func (s *Server) cephBootstrap(ctx context.Context, req cephBootstrapReq, j *jobs.Job) error {
	c := s.clusterState()
	members := c.Members
	n := len(members)
	if n < 3 && !req.Test {
		return fmt.Errorf("a Ceph cluster needs 3 NAS at least (or choose the test mode)")
	}
	j.Logf("Checking the %d NAS", n)
	infos := make([]clusterPeerInfo, n)
	for i, m := range members {
		info, err := s.memberInfo(ctx, m)
		if err != nil {
			return fmt.Errorf("%s does not answer: %w", m.Name, err)
		}
		switch {
		case !info.CephInstalled:
			return fmt.Errorf("install the Ceph component on %s first (Settings → Components)", m.Name)
		case info.CephNode:
			return fmt.Errorf("%s is already a Ceph node", m.Name)
		case info.Addr == "":
			return fmt.Errorf("%s has no address in the network %s", m.Name, c.PublicNet)
		case cluster.ValidDaemonName(info.Name) != nil:
			return cluster.ValidDaemonName(info.Name)
		}
		infos[i] = info
	}
	// Disks: checked on each NAS again when the OSD is created.
	totalDisks := 0
	for i, m := range members {
		paths := req.Disks[m.ID]
		if len(paths) == 0 {
			continue
		}
		var disks []ceph.Disk
		if err := s.clusterDisks(ctx, m, &disks); err != nil {
			return err
		}
		for _, p := range paths {
			k := slices.IndexFunc(disks, func(d ceph.Disk) bool { return d.Path == p })
			if k < 0 {
				return fmt.Errorf("disk %s not found on %s", p, infos[i].Name)
			}
			if !disks[k].Eligible {
				return fmt.Errorf("disk %s of %s cannot be used: %s", p, infos[i].Name, disks[k].Reason)
			}
			if disks[k].Erase && !req.Erase {
				return fmt.Errorf("disk %s of %s holds data: confirm that it may be erased", p, infos[i].Name)
			}
			totalDisks++
		}
	}
	if totalDisks == 0 {
		return fmt.Errorf("choose at least one disk")
	}
	size, minSize := req.Size, req.MinSize
	if size == 0 {
		size = min(3, n)
		if n == 1 && totalDisks > 1 {
			size = min(2, totalDisks)
		}
	}
	if minSize == 0 {
		minSize = ceph.DefaultMinSize(size)
	}
	if size < 1 || size > 7 || minSize < 1 || minSize > size {
		return fmt.Errorf("invalid number of copies")
	}
	if size > n && n > 1 {
		return fmt.Errorf("%d copies need %d NAS: there are %d", size, size, n)
	}
	if size < 2 && !req.Test {
		return fmt.Errorf("a single copy is only allowed in test mode")
	}
	rbdPool, fsName := firstNonEmpty(req.RBDPool, "rbd"), firstNonEmpty(req.FSName, "cephfs")
	if req.RBD {
		if err := ceph.ValidPool(rbdPool); err != nil {
			return err
		}
	}
	if req.CephFS {
		if err := ceph.ValidPool(fsName); err != nil {
			return err
		}
	}

	// Monitors: three (five from five NAS), on the first members.
	nm := min(n, 3)
	if n >= 5 {
		nm = 5
	}
	l := store.CephLocal{FSID: ceph.NewFSID(), Test: req.Test, Size: size, MinSize: minSize, Created: time.Now(), PublicNet: c.PublicNet, ClusterNet: c.PrivateNet, SingleHost: n == 1}
	mons := make([]ceph.Mon, nm)
	for i := 0; i < nm; i++ {
		l.Mons = append(l.Mons, store.CephMon{Name: infos[i].Name, Addr: infos[i].Addr})
		mons[i] = ceph.Mon{Name: infos[i].Name, Addr: infos[i].Addr}
	}
	for _, info := range infos {
		if info.MemMB > 0 && info.MemMB < 6144 {
			l.SmallMemory = true
		}
	}
	if l.SmallMemory {
		j.Logf("Some NAS have less than 6 GB of memory: the caches of the Ceph daemons are kept small")
	}
	adminKey, monKey, bootKey := ceph.NewKey(), ceph.NewKey(), ceph.NewKey()
	admin := ceph.Entity{Name: "client.admin", Key: adminKey, Caps: ceph.CapsAdmin}
	boot := ceph.Entity{Name: "client.bootstrap-osd", Key: bootKey, Caps: ceph.CapsBootstrapOSD}
	monKeyring := ceph.Keyring(ceph.Entity{Name: "mon.", Key: monKey, Caps: ceph.CapsMon}, admin, boot)
	conf := cephConfOf(l)
	j.Logf("New cluster %s, monitors on %s, %d copies (at least %d to keep writing)", l.FSID, names(l.Mons), size, minSize)
	monmap, err := ceph.MonMap(ctx, l.FSID, mons)
	if err != nil {
		return err
	}
	j.SetProgress(5)
	for _, m := range members {
		j.Logf("Configuration and keys on %s", m.Name)
		if err := s.cephNode(ctx, m, "base", map[string]any{"conf": conf, "admin": ceph.Keyring(admin), "bootstrap": ceph.Keyring(boot), "local": l}, nil, time.Minute); err != nil {
			return err
		}
	}
	for i := 0; i < nm; i++ {
		j.Logf("Monitor on %s (%s)", infos[i].Name, infos[i].Addr)
		if err := s.cephNode(ctx, members[i], "mon", map[string]any{"addr": infos[i].Addr, "monmap": base64.StdEncoding.EncodeToString(monmap), "keyring": monKeyring}, nil, 5*time.Minute); err != nil {
			return err
		}
		s.setClusterRoles(ctx, members[i].ID, []string{"mon"}, nil)
	}
	j.SetProgress(20)
	j.Logf("Waiting for the monitors to agree (quorum)…")
	if err := waitFor(ctx, 5*time.Minute, func() (bool, error) {
		st, err := ceph.Local.Status(ctx)
		if err != nil {
			return false, nil
		}
		return len(st.Quorum) == nm, nil
	}); err != nil {
		return fmt.Errorf("the monitors did not form a quorum: check that the NAS reach each other on %s (ports 3300 and 6789)", c.PublicNet)
	}
	for _, m := range members {
		j.Logf("Manager on %s", m.Name)
		if err := s.cephNode(ctx, m, "mgr", nil, nil, 2*time.Minute); err != nil {
			return err
		}
		s.setClusterRoles(ctx, m.ID, []string{"mgr"}, nil)
	}
	j.SetProgress(30)
	if err := s.cephCreateOSDs(ctx, j, members, req.Disks, req.Erase, 30, 80); err != nil {
		return err
	}
	j.Logf("Waiting for the disks to come up…")
	_ = waitFor(ctx, 3*time.Minute, func() (bool, error) {
		st, err := ceph.Local.Status(ctx)
		return err == nil && st.OSDsUp >= totalDisks, nil
	})
	cephMuteKnown(ctx)
	if req.RBD {
		if err := s.cephCreatePool(ctx, j, "rbd", rbdPool); err != nil {
			return err
		}
	}
	j.SetProgress(90)
	if req.CephFS {
		if err := s.cephCreatePool(ctx, j, "cephfs", fsName); err != nil {
			return err
		}
	}
	j.SetProgress(100)
	j.Logf("The Ceph cluster is ready")
	return nil
}

// clusterDisks lists the disks of a member for OSDs.
func (s *Server) clusterDisks(ctx context.Context, m store.ClusterMember, out *[]ceph.Disk) error {
	if m.ID == s.clusterState().Self {
		d, err := s.cephLocalDisks()
		*out = d
		return err
	}
	return s.clusterCall(ctx, m, time.Minute, "disks", nil, out)
}

func names(mons []store.CephMon) string {
	var n []string
	for _, m := range mons {
		n = append(n, m.Name)
	}
	return strings.Join(n, ", ")
}

func waitFor(ctx context.Context, d time.Duration, ok func() (bool, error)) error {
	deadline := time.Now().Add(d)
	for {
		done, err := ok()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// cephCreateOSDs creates the OSDs of several members (members side by side,
// the disks of one member one after the other).
func (s *Server) cephCreateOSDs(ctx context.Context, j *jobs.Job, members []store.ClusterMember, disks map[string][]string, erase bool, from, to float64) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs, created []string
	total, done := 0, 0
	for _, m := range members {
		total += len(disks[m.ID])
	}
	for _, m := range members {
		if len(disks[m.ID]) == 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, d := range disks[m.ID] {
				j.Logf("Creating the OSD on %s of %s…", d, m.Name)
				var res struct {
					Log string `json:"log"`
				}
				err := s.cephNode(ctx, m, "osd", map[string]any{"device": d, "erase": erase}, &res, 20*time.Minute)
				mu.Lock()
				done++
				j.SetProgress(from + (to-from)*float64(done)/float64(max(total, 1)))
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s %s: %v", m.Name, d, err))
					j.Logf("OSD on %s of %s failed: %v", d, m.Name, err)
				} else {
					j.Logf("OSD on %s of %s created", d, m.Name)
					created = append(created, m.ID)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	// The new OSDs reach the CRUSH map only once started: take the roles
	// from what was created.
	slices.Sort(created)
	for _, id := range slices.Compact(created) {
		s.setClusterRoles(ctx, id, []string{"osd"}, nil)
	}
	if len(errs) > 0 {
		return fmt.Errorf("some OSDs could not be created: %s", strings.Join(errs, "; "))
	}
	return nil
}

// setRolesFromOSDs gives the osd role to the members that hold OSDs.
func (s *Server) setRolesFromOSDs(ctx context.Context) {
	osds, err := s.cephOSDs(ctx)
	if err != nil {
		return
	}
	hosts := map[string]bool{}
	for _, o := range osds {
		if o.Status != "destroyed" {
			hosts[o.Host] = true
		}
	}
	for _, m := range s.clusterState().Members {
		has := slices.Contains(m.Roles, "osd")
		switch {
		case hosts[m.Name] && !has:
			s.setClusterRoles(ctx, m.ID, []string{"osd"}, nil)
		case !hosts[m.Name] && has:
			s.setClusterRoles(ctx, m.ID, nil, []string{"osd"})
		}
	}
}

// cephCreatePool creates an RBD pool or a CephFS filesystem (with its
// metadata servers on up to three nodes).
func (s *Server) cephCreatePool(ctx context.Context, j *jobs.Job, kind, name string) error {
	if err := ceph.ValidPool(name); err != nil {
		return err
	}
	switch kind {
	case "rbd":
		j.Logf("RBD pool %s", name)
		if _, err := ceph.Local.Ceph(ctx, "osd", "pool", "create", name, strconv.Itoa(s.cephPGs(ctx, 32))); err != nil {
			return err
		}
		if _, err := ceph.Local.Ceph(ctx, "osd", "pool", "application", "enable", name, "rbd"); err != nil {
			return err
		}
		if _, err := ceph.Local.RBD(ctx, "pool", "init", name); err != nil {
			return err
		}
	case "cephfs":
		// Pools and filesystem made here rather than by "fs volume create",
		// which needs the manager's volumes module to be up.
		j.Logf("CephFS filesystem %s", name)
		meta, data := "cephfs."+name+".meta", "cephfs."+name+".data"
		for p, n := range map[string]int{meta: 16, data: 32} {
			if _, err := ceph.Local.Ceph(ctx, "osd", "pool", "create", p, strconv.Itoa(s.cephPGs(ctx, n))); err != nil {
				return err
			}
		}
		// The metadata pool is small but hot: let the autoscaler favour it.
		_, _ = ceph.Local.Ceph(ctx, "osd", "pool", "set", meta, "pg_autoscale_bias", "4")
		if _, err := ceph.Local.Ceph(ctx, "fs", "new", name, meta, data); err != nil {
			return err
		}
		n := 0
		for _, m := range s.cephMembers() {
			if n == 3 {
				break
			}
			if slices.Contains(m.Roles, "mds") {
				n++
				continue
			}
			j.Logf("Metadata server on %s", m.Name)
			if err := s.cephNode(ctx, m, "mds", nil, nil, 2*time.Minute); err != nil {
				return err
			}
			s.setClusterRoles(ctx, m.ID, []string{"mds"}, nil)
			n++
		}
	default:
		return fmt.Errorf("unknown pool kind")
	}
	return nil
}

// cephPGs is the number of placement groups a new pool starts with. The
// autoscaler adjusts it when it runs, but the manager of Debian 13 cannot load
// its Python modules (see cephMuteKnown): start with a sensible number, lower
// on clusters with few disks.
func (s *Server) cephPGs(ctx context.Context, want int) int {
	if st, err := ceph.Local.Status(ctx); err == nil && st.OSDs > 0 && st.OSDs < 3 {
		return max(want/4, 8)
	}
	return want
}

// cephMuteKnown mutes the health warning of the manager modules that the
// Python of Debian 13 cannot load (PyO3 extensions in sub-interpreters, a
// known Debian/Ceph 18 problem): storage is not affected, but the warning
// would hide real problems behind a permanent HEALTH_WARN.
func cephMuteKnown(ctx context.Context) {
	var raw []byte
	if ceph.Local.JSON(ctx, &raw, "health", "detail") != nil {
		return
	}
	if ceph.PyO3Only(raw) {
		_, _ = ceph.Local.Ceph(ctx, "health", "mute", "MGR_MODULE_DEPENDENCY", "--sticky")
	}
}

// cephOSDs reads the OSDs with their usage and disk.
func (s *Server) cephOSDs(ctx context.Context) ([]ceph.OSD, error) {
	var raw []byte
	if err := ceph.Local.JSON(ctx, &raw, "osd", "tree"); err != nil {
		return nil, err
	}
	osds, err := ceph.ParseOSDTree(raw)
	if err != nil {
		return nil, err
	}
	if ceph.Local.JSON(ctx, &raw, "osd", "df") == nil {
		_ = ceph.MergeOSDDF(osds, raw)
	}
	if ceph.Local.JSON(ctx, &raw, "osd", "metadata") == nil {
		_ = ceph.MergeOSDMetadata(osds, raw)
	}
	if osds == nil {
		osds = []ceph.OSD{}
	}
	return osds, nil
}

// cephMemberByName finds the member whose host name is name.
func (s *Server) cephMemberByName(name string) (store.ClusterMember, bool) {
	for _, m := range s.clusterState().Members {
		if m.Name == name {
			return m, true
		}
	}
	return store.ClusterMember{}, false
}

func (s *Server) cephOSD(ctx context.Context, id int) (ceph.OSD, store.ClusterMember, error) {
	osds, err := s.cephOSDs(ctx)
	if err != nil {
		return ceph.OSD{}, store.ClusterMember{}, err
	}
	for _, o := range osds {
		if o.ID == id {
			m, ok := s.cephMemberByName(o.Host)
			if !ok {
				return o, m, badRequest(fmt.Sprintf("osd.%d is on %s, which is not a member of the cluster", id, o.Host))
			}
			return o, m, nil
		}
	}
	return ceph.OSD{}, store.ClusterMember{}, notFound("OSD not found")
}

// cephPushConf sends the configuration of the local cluster to every node.
func (s *Server) cephPushConf(ctx context.Context, l store.CephLocal) []string {
	var warn []string
	for _, m := range s.cephMembers() {
		if err := s.cephNode(ctx, m, "conf", map[string]any{"local": l}, nil, 30*time.Second); err != nil {
			warn = append(warn, m.Name+": "+err.Error())
		}
	}
	return warn
}

func (s *Server) cephRoutes() {
	s.cephPeerRoutes()
	s.cephStorageRoutes()

	s.admin("GET /api/ceph", func(r *http.Request) (any, error) {
		out := map[string]any{"installed": ceph.Installed(), "clientInstalled": ceph.ClientInstalled(), "version": ceph.Version()}
		c := s.clusterState()
		if c.ID != "" {
			out["cluster"] = maskCluster(c)
		} else {
			out["cluster"] = nil
		}
		l := s.cephLocal()
		out["local"] = l
		if l == nil {
			return out, nil
		}
		// A loaded cluster can take a while to answer: wait for the status
		// (the monitors' own timeout is shorter), then always say why it is missing.
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		out["status"] = nil
		st, err := ceph.Local.Status(ctx)
		if err != nil {
			out["error"] = err.Error()
			return out, nil
		}
		out["status"] = st
		var wg sync.WaitGroup
		var mu sync.Mutex
		set := func(k string, v any) { mu.Lock(); out[k] = v; mu.Unlock() }
		wg.Add(5)
		go func() {
			defer wg.Done()
			if osds, err := s.cephOSDs(ctx); err == nil {
				set("osds", osds)
			}
		}()
		go func() {
			defer wg.Done()
			var raw []byte
			daemons := []ceph.Daemon{}
			if ceph.Local.JSON(ctx, &raw, "mon", "dump") == nil {
				if d, err := ceph.ParseMonDump(raw, st.Quorum); err == nil {
					daemons = append(daemons, d...)
				}
			}
			if ceph.Local.JSON(ctx, &raw, "mgr", "dump") == nil {
				if d, err := ceph.ParseMgrDump(raw); err == nil {
					daemons = append(daemons, d...)
				}
			}
			if ceph.Local.JSON(ctx, &raw, "fs", "dump") == nil {
				if d, err := ceph.ParseFSDump(raw); err == nil {
					daemons = append(daemons, d...)
				}
			}
			set("daemons", daemons)
		}()
		go func() {
			defer wg.Done()
			if pools, fss, err := ceph.Local.Pools(ctx); err == nil {
				set("pools", pools)
				set("filesystems", fss)
			}
		}()
		go func() {
			defer wg.Done()
			var raw []byte
			if ceph.Local.JSON(ctx, &raw, "osd", "dump") == nil {
				set("noout", ceph.HasFlag(raw, "noout"))
			}
		}()
		go func() {
			defer wg.Done()
			set("clients", s.cephClients(ctx))
		}()
		wg.Wait()
		return out, nil
	})

	s.admin("GET /api/ceph/disks", func(r *http.Request) (any, error) {
		type row struct {
			Member string      `json:"member"`
			Name   string      `json:"name"`
			Disks  []ceph.Disk `json:"disks"`
			Error  string      `json:"error,omitempty"`
		}
		c := s.clusterState()
		out := make([]row, len(c.Members))
		var wg sync.WaitGroup
		for i, m := range c.Members {
			out[i] = row{Member: m.ID, Name: m.Name, Disks: []ceph.Disk{}}
			wg.Add(1)
			go func() {
				defer wg.Done()
				var d []ceph.Disk
				if err := s.clusterDisks(r.Context(), m, &d); err != nil {
					out[i].Error = err.Error()
				} else if d != nil {
					out[i].Disks = d
				}
			}()
		}
		wg.Wait()
		return out, nil
	})

	s.admin("POST /api/ceph/bootstrap", func(r *http.Request) (any, error) {
		var req cephBootstrapReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		c := s.clusterState()
		if c.ID == "" {
			return nil, badRequest("create the cluster of NAS first")
		}
		if s.cephLocal() != nil {
			return nil, badRequest("the Ceph cluster already exists")
		}
		if !ceph.Installed() {
			return nil, badRequest("install the Ceph component first (Settings → Components)")
		}
		if len(c.Members) < 3 && !req.Test {
			return nil, badRequest("a Ceph cluster needs 3 NAS at least (or choose the test mode)")
		}
		s.audit(r, "ceph", fmt.Sprintf("Creating the Ceph cluster on %d NAS", len(c.Members)))
		return s.cephJob("Creating the Ceph cluster", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			err := s.cephBootstrap(ctx, req, j)
			if err != nil {
				j.Logf("Creation failed. Destroy the Ceph cluster from the Ceph page to clean up, then try again.")
				s.store.Log("error", "ceph", userOf(r), "Creating the Ceph cluster failed: "+err.Error())
				return err
			}
			s.store.Log("info", "ceph", userOf(r), "Ceph cluster created")
			return nil
		})
	})

	// Adds a member of the cluster as a Ceph node (manager, optional monitor
	// and metadata server, its disks).
	s.admin("POST /api/ceph/nodes/{id}", func(r *http.Request) (any, error) {
		var req struct {
			Mon   bool     `json:"mon"`
			MDS   bool     `json:"mds"`
			Disks []string `json:"disks"`
			Erase bool     `json:"erase"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		l := s.cephLocal()
		if l == nil {
			return nil, badRequest("this NAS is not a Ceph node")
		}
		m, ok := cluster.Member(s.clusterState(), r.PathValue("id"))
		if !ok {
			return nil, notFound("member not found")
		}
		s.audit(r, "ceph", "Adding "+m.Name+" to the Ceph cluster")
		return s.cephJob("Adding "+m.Name+" to Ceph", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			info, err := s.memberInfo(ctx, m)
			if err != nil {
				return err
			}
			if !info.CephInstalled {
				return fmt.Errorf("install the Ceph component on %s first", m.Name)
			}
			if info.Addr == "" {
				return fmt.Errorf("%s has no address in the cluster network", m.Name)
			}
			if !info.CephNode {
				admin, err := readFile(ceph.AdminKeyring)
				if err != nil {
					return err
				}
				boot, _ := readFile(ceph.BootstrapOSD)
				j.Logf("Configuration and keys on %s", m.Name)
				if err := s.cephNode(ctx, m, "base", map[string]any{"conf": cephConfOf(*l), "admin": admin, "bootstrap": boot, "local": l}, nil, time.Minute); err != nil {
					return err
				}
			}
			if req.Mon && !slices.ContainsFunc(l.Mons, func(x store.CephMon) bool { return x.Name == info.Name }) {
				if err := s.cephAddMon(ctx, j, m, info, l); err != nil {
					return err
				}
			}
			if !slices.Contains(info.Daemons, "mgr") {
				j.Logf("Manager on %s", m.Name)
				if err := s.cephNode(ctx, m, "mgr", nil, nil, 2*time.Minute); err != nil {
					return err
				}
				s.setClusterRoles(ctx, m.ID, []string{"mgr"}, nil)
			}
			if req.MDS && !slices.Contains(info.Daemons, "mds") {
				j.Logf("Metadata server on %s", m.Name)
				if err := s.cephNode(ctx, m, "mds", nil, nil, 2*time.Minute); err != nil {
					return err
				}
				s.setClusterRoles(ctx, m.ID, []string{"mds"}, nil)
			}
			return s.cephCreateOSDs(ctx, j, []store.ClusterMember{m}, map[string][]string{m.ID: req.Disks}, req.Erase, 50, 100)
		})
	})

	// Takes a node out of Ceph: its OSDs must have been removed.
	s.admin("DELETE /api/ceph/nodes/{id}", func(r *http.Request) (any, error) {
		l := s.cephLocal()
		if l == nil {
			return nil, badRequest("this NAS is not a Ceph node")
		}
		c := s.clusterState()
		m, ok := cluster.Member(c, r.PathValue("id"))
		if !ok {
			return nil, notFound("member not found")
		}
		if m.ID == c.Self {
			return nil, badRequest("remove this NAS from another node of the cluster")
		}
		s.audit(r, "ceph", "Removing "+m.Name+" from the Ceph cluster")
		return s.cephJob("Removing "+m.Name+" from Ceph", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			osds, err := s.cephOSDs(ctx)
			if err != nil {
				return err
			}
			for _, o := range osds {
				if o.Host == m.Name {
					return fmt.Errorf("%s still holds osd.%d: remove its disks from Ceph first", m.Name, o.ID)
				}
			}
			var info clusterPeerInfo
			if i, err := s.memberInfo(ctx, m); err == nil {
				info = i
				if len(i.CephUse) > 0 {
					return fmt.Errorf("%s still uses this Ceph cluster (%s): remove that first", m.Name, strings.Join(i.CephUse, ", "))
				}
			}
			if slices.ContainsFunc(l.Mons, func(x store.CephMon) bool { return x.Name == m.Name }) {
				if len(l.Mons) <= 1 {
					return fmt.Errorf("%s runs the only monitor", m.Name)
				}
				j.Logf("Removing the monitor of %s", m.Name)
				if _, err := ceph.Local.Ceph(ctx, "mon", "remove", m.Name); err != nil {
					return err
				}
				l.Mons = slices.DeleteFunc(l.Mons, func(x store.CephMon) bool { return x.Name == m.Name })
				_ = s.cephNode(ctx, m, "daemon-remove", map[string]string{"kind": "mon"}, nil, time.Minute)
			}
			for _, k := range []string{"mds", "mgr"} {
				_ = s.cephNode(ctx, m, "daemon-remove", map[string]string{"kind": k}, nil, time.Minute)
			}
			if info.CephNode {
				if err := s.cephNode(ctx, m, "purge", nil, nil, 5*time.Minute); err != nil {
					j.Logf("Clean-up on %s: %v", m.Name, err)
				}
			}
			_ = s.store.Update(func(st *store.State) error { st.Ceph.Local.Mons = l.Mons; return nil })
			for _, w := range s.cephPushConf(ctx, *l) {
				j.Logf("Configuration not updated on %s", w)
			}
			s.setClusterRoles(ctx, m.ID, nil, []string{"mon", "mgr", "mds", "osd"})
			j.Logf("%s is no longer a Ceph node", m.Name)
			return nil
		})
	})

	s.admin("POST /api/ceph/osds", func(r *http.Request) (any, error) {
		var req struct {
			Member string `json:"member"`
			Device string `json:"device"`
			Erase  bool   `json:"erase"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if s.cephLocal() == nil {
			return nil, badRequest("this NAS is not a Ceph node")
		}
		m, ok := cluster.Member(s.clusterState(), req.Member)
		if !ok || len(m.Roles) == 0 && m.ID != s.clusterState().Self {
			return nil, badRequest("this NAS is not a Ceph node: add it to Ceph first")
		}
		s.audit(r, "ceph", fmt.Sprintf("Adding the disk %s of %s to Ceph", req.Device, m.Name))
		return s.cephJob("Adding a disk to Ceph", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return s.cephCreateOSDs(ctx, j, []store.ClusterMember{m}, map[string][]string{m.ID: {req.Device}}, req.Erase, 0, 100)
		})
	})

	// Safe removal: out, wait until its data is copied elsewhere, then destroy.
	s.admin("DELETE /api/ceph/osds/{id}", func(r *http.Request) (any, error) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id < 0 {
			return nil, badRequest("invalid OSD")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		o, m, err := s.cephOSD(ctx, id)
		if err != nil {
			return nil, err
		}
		l := s.cephLocal()
		osds, _ := s.cephOSDs(ctx)
		left := 0
		hosts := map[string]bool{}
		for _, x := range osds {
			if x.ID != id && x.Status != "destroyed" {
				left++
				hosts[x.Host] = true
			}
		}
		if l != nil && (left < l.Size || !l.SingleHost && len(hosts) < l.Size) && r.URL.Query().Get("force") != "1" {
			return nil, badRequest(fmt.Sprintf("without this disk, Ceph cannot keep %d copies of the data: add a disk first", l.Size))
		}
		s.audit(r, "ceph", fmt.Sprintf("Removing osd.%d (%s of %s)", id, o.Device, m.Name))
		return s.cephJob(fmt.Sprintf("Removing osd.%d", id), userOf(r), func(ctx context.Context, j *jobs.Job) error {
			j.Logf("osd.%d marked out: Ceph copies its data to the other disks", id)
			if _, err := ceph.Local.Ceph(ctx, "osd", "out", strconv.Itoa(id)); err != nil {
				return err
			}
			if err := s.cephWaitSafe(ctx, j, id); err != nil {
				return err
			}
			return s.cephDestroyOSD(ctx, j, id, m, false)
		})
	})

	// Replaces the disk of an OSD (failed, or to be swapped): the OSD keeps
	// its number and place, Ceph fills the new disk.
	s.admin("POST /api/ceph/osds/{id}/replace", func(r *http.Request) (any, error) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id < 0 {
			return nil, badRequest("invalid OSD")
		}
		var req struct {
			Device string `json:"device"`
			Erase  bool   `json:"erase"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		o, m, err := s.cephOSD(ctx, id)
		if err != nil {
			return nil, err
		}
		s.audit(r, "ceph", fmt.Sprintf("Replacing the disk of osd.%d on %s by %s", id, m.Name, req.Device))
		return s.cephJob(fmt.Sprintf("Replacing the disk of osd.%d", id), userOf(r), func(ctx context.Context, j *jobs.Job) error {
			if o.Status == "up" {
				j.Logf("osd.%d still works: its data is copied elsewhere first", id)
				if _, err := ceph.Local.Ceph(ctx, "osd", "out", strconv.Itoa(id)); err != nil {
					return err
				}
				if err := s.cephWaitSafe(ctx, j, id); err != nil {
					return err
				}
			}
			if err := s.cephDestroyOSD(ctx, j, id, m, true); err != nil {
				return err
			}
			j.Logf("New disk %s for osd.%d", req.Device, id)
			var res struct {
				Log string `json:"log"`
			}
			if err := s.cephNode(ctx, m, "osd", map[string]any{"device": req.Device, "erase": req.Erase, "id": id}, &res, 20*time.Minute); err != nil {
				return err
			}
			j.Logf("osd.%d is back: Ceph copies the data to it", id)
			return nil
		})
	})

	s.admin("POST /api/ceph/osds/{id}/{action}", func(r *http.Request) (any, error) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id < 0 {
			return nil, badRequest("invalid OSD")
		}
		action := r.PathValue("action")
		if action != "in" && action != "out" {
			return nil, notFound("unknown action")
		}
		if _, err := ceph.Local.Ceph(r.Context(), "osd", action, strconv.Itoa(id)); err != nil {
			return nil, err
		}
		s.audit(r, "ceph", fmt.Sprintf("osd.%d marked %s", id, action))
		return nil, nil
	})

	// Maintenance: Ceph does not move data while NAS are restarted.
	s.admin("POST /api/ceph/maintenance", func(r *http.Request) (any, error) {
		var req struct {
			On bool `json:"on"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		verb := "unset"
		if req.On {
			verb = "set"
		}
		if _, err := ceph.Local.Ceph(r.Context(), "osd", verb, "noout"); err != nil {
			return nil, err
		}
		if req.On {
			s.audit(r, "ceph", "Ceph maintenance mode on (noout)")
		} else {
			s.audit(r, "ceph", "Ceph maintenance mode off")
		}
		return nil, nil
	})

	s.admin("POST /api/ceph/pools", func(r *http.Request) (any, error) {
		var req struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if s.cephLocal() == nil {
			return nil, badRequest("this NAS is not a Ceph node")
		}
		if err := ceph.ValidPool(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		if req.Kind != "rbd" && req.Kind != "cephfs" {
			return nil, badRequest("unknown pool kind")
		}
		s.audit(r, "ceph", "Creating the "+req.Kind+" pool "+req.Name)
		return s.cephJob("Creating the Ceph pool "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return s.cephCreatePool(ctx, j, req.Kind, req.Name)
		})
	})

	s.admin("DELETE /api/ceph/pools/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		if err := ceph.ValidPool(name); err != nil {
			return nil, badRequest(err.Error())
		}
		if r.URL.Query().Get("confirm") != name {
			return nil, badRequest("type the name of the pool to confirm")
		}
		if err := s.cephPoolUnused(r.Context(), name, ""); err != nil {
			return nil, err
		}
		if _, err := ceph.Local.Ceph(r.Context(), "osd", "pool", "rm", name, name, "--yes-i-really-really-mean-it"); err != nil {
			return nil, err
		}
		s.audit(r, "ceph", "Deleted the Ceph pool "+name)
		return nil, nil
	})

	s.admin("DELETE /api/ceph/filesystems/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		if err := ceph.ValidPool(name); err != nil {
			return nil, badRequest(err.Error())
		}
		if r.URL.Query().Get("confirm") != name {
			return nil, badRequest("type the name of the filesystem to confirm")
		}
		if err := s.cephPoolUnused(r.Context(), "", name); err != nil {
			return nil, err
		}
		var raw []byte
		var pools []string
		if ceph.Local.JSON(r.Context(), &raw, "fs", "ls") == nil {
			if fss, err := ceph.ParseFSList(raw); err == nil {
				for _, f := range fss {
					if f.Name == name {
						pools = append([]string{f.MetadataPool}, f.DataPools...)
					}
				}
			}
		}
		if _, err := ceph.Local.Ceph(r.Context(), "fs", "fail", name); err != nil {
			return nil, err
		}
		if _, err := ceph.Local.Ceph(r.Context(), "fs", "rm", name, "--yes-i-really-mean-it"); err != nil {
			return nil, err
		}
		for _, p := range pools {
			if _, err := ceph.Local.Ceph(r.Context(), "osd", "pool", "rm", p, p, "--yes-i-really-really-mean-it"); err != nil {
				return nil, err
			}
		}
		s.audit(r, "ceph", "Deleted the CephFS filesystem "+name)
		return nil, nil
	})

	// Client keys for other NAS (or any Ceph client) using this cluster.
	s.admin("POST /api/ceph/clients", func(r *http.Request) (any, error) {
		var req struct {
			Name string `json:"name"`
			Pool string `json:"pool"`
			FS   string `json:"fs"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		l := s.cephLocal()
		if l == nil {
			return nil, badRequest("this NAS is not a Ceph node")
		}
		if err := ceph.ValidUser(req.Name); err != nil || req.Name == "admin" || strings.HasPrefix(req.Name, "bootstrap") {
			return nil, badRequest("invalid user name")
		}
		caps, err := ceph.ClientCaps(req.Pool, req.FS)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		if _, err := ceph.Local.Ceph(r.Context(), "auth", "get", "client."+req.Name); err == nil {
			return nil, badRequest("this user already exists")
		}
		out, err := ceph.Local.Ceph(r.Context(), append([]string{"auth", "get-or-create", "client." + req.Name}, caps...)...)
		if err != nil {
			return nil, err
		}
		key := ceph.KeyOf(out, "client."+req.Name)
		if key == "" {
			return nil, fmt.Errorf("the key was not created")
		}
		var mons []string
		for _, m := range l.Mons {
			mons = append(mons, m.Addr)
		}
		s.audit(r, "ceph", "Created the Ceph client key client."+req.Name)
		// The key is shown once, to be copied to the other NAS.
		return map[string]any{"user": req.Name, "key": key, "fsid": l.FSID, "mons": mons}, nil
	})

	s.admin("DELETE /api/ceph/clients/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		if err := ceph.ValidUser(name); err != nil || name == "admin" || name == "strata-vms" || strings.HasPrefix(name, "bootstrap") {
			return nil, badRequest("invalid user name")
		}
		if _, err := ceph.Local.Ceph(r.Context(), "auth", "del", "client."+name); err != nil {
			return nil, err
		}
		s.audit(r, "ceph", "Deleted the Ceph client key client."+name)
		return nil, nil
	})

	// Destroys the Ceph cluster: every OSD is erased on every node.
	s.admin("DELETE /api/ceph", func(r *http.Request) (any, error) {
		l := s.cephLocal()
		if l == nil {
			return nil, badRequest("this NAS is not a Ceph node")
		}
		c := s.clusterState()
		if r.URL.Query().Get("confirm") != c.Name {
			return nil, badRequest("type the name of the cluster to confirm")
		}
		s.audit(r, "ceph", "Destroying the Ceph cluster")
		return s.cephJob("Destroying the Ceph cluster", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			nodes := s.cephMembers()
			for _, m := range nodes {
				if info, err := s.memberInfo(ctx, m); err == nil && len(info.CephUse) > 0 {
					return fmt.Errorf("%s still uses this Ceph cluster (%s): remove that first", m.Name, strings.Join(info.CephUse, ", "))
				}
			}
			var failed []string
			// Every node but this one first: this NAS keeps the admin key to the end.
			sort.SliceStable(nodes, func(i, k int) bool { return nodes[k].ID == c.Self && nodes[i].ID != c.Self })
			for _, m := range nodes {
				j.Logf("Removing Ceph from %s (its Ceph disks are erased)", m.Name)
				if err := s.cephNode(ctx, m, "purge", nil, nil, 10*time.Minute); err != nil {
					j.Logf("%s: %v", m.Name, err)
					failed = append(failed, m.Name)
					continue
				}
				s.setClusterRoles(ctx, m.ID, nil, []string{"mon", "mgr", "mds", "osd"})
			}
			if len(failed) > 0 {
				return fmt.Errorf("Ceph could not be removed from %s: try again once they are reachable", strings.Join(failed, ", "))
			}
			s.store.Log("warning", "ceph", userOf(r), "The Ceph cluster was destroyed")
			return nil
		})
	})
}

func readFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}

// cephAddMon adds a monitor on member m of the running cluster.
func (s *Server) cephAddMon(ctx context.Context, j *jobs.Job, m store.ClusterMember, info clusterPeerInfo, l *store.CephLocal) error {
	j.Logf("Monitor on %s (%s)", m.Name, info.Addr)
	tmp, err := os.MkdirTemp("", "strata-mon")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if _, err := ceph.Local.Ceph(ctx, "mon", "getmap", "-o", tmp+"/monmap"); err != nil {
		return err
	}
	if _, err := ceph.Local.Ceph(ctx, "auth", "get", "mon.", "-o", tmp+"/keyring"); err != nil {
		return err
	}
	mm, err := os.ReadFile(tmp + "/monmap")
	if err != nil {
		return err
	}
	kr, err := os.ReadFile(tmp + "/keyring")
	if err != nil {
		return err
	}
	if err := s.cephNode(ctx, m, "mon", map[string]any{"addr": info.Addr, "monmap": base64.StdEncoding.EncodeToString(mm), "keyring": string(kr)}, nil, 5*time.Minute); err != nil {
		return err
	}
	_ = waitFor(ctx, 3*time.Minute, func() (bool, error) {
		st, err := ceph.Local.Status(ctx)
		return err == nil && slices.Contains(st.Quorum, info.Name), nil
	})
	l.Mons = append(l.Mons, store.CephMon{Name: info.Name, Addr: info.Addr})
	_ = s.store.Update(func(st *store.State) error {
		if st.Ceph.Local != nil {
			st.Ceph.Local.Mons = l.Mons
		}
		return nil
	})
	for _, w := range s.cephPushConf(ctx, *l) {
		j.Logf("Configuration not updated on %s", w)
	}
	s.setClusterRoles(ctx, m.ID, []string{"mon"}, nil)
	return nil
}

// cephWaitSafe waits until an OSD marked out holds no data Ceph still needs.
func (s *Server) cephWaitSafe(ctx context.Context, j *jobs.Job, id int) error {
	j.Logf("Waiting until its data is safe elsewhere (this can take hours with a lot of data)…")
	last := time.Time{}
	return waitFor(ctx, 72*time.Hour, func() (bool, error) {
		if _, err := ceph.Local.Ceph(ctx, "osd", "safe-to-destroy", fmt.Sprintf("osd.%d", id)); err == nil {
			return true, nil
		}
		if time.Since(last) > time.Minute {
			last = time.Now()
			if st, err := ceph.Local.Status(ctx); err == nil {
				j.Logf("Data still moving: %.1f%% misplaced, %.1f%% degraded", st.MisplacedRatio*100, st.DegradedRatio*100)
			}
		}
		return false, nil
	})
}

// cephDestroyOSD stops an OSD on its node and removes it from the cluster;
// keepID keeps its number for a replacement disk.
func (s *Server) cephDestroyOSD(ctx context.Context, j *jobs.Job, id int, m store.ClusterMember, keepID bool) error {
	if err := s.cephNode(ctx, m, "osd-stop", map[string]int{"id": id}, nil, time.Minute); err != nil {
		j.Logf("%s: %v (continuing: the disk may be gone)", m.Name, err)
	}
	if keepID {
		if _, err := ceph.Local.Ceph(ctx, "osd", "destroy", strconv.Itoa(id), "--yes-i-really-mean-it"); err != nil {
			return err
		}
	} else if _, err := ceph.Local.Ceph(ctx, "osd", "purge", strconv.Itoa(id), "--yes-i-really-mean-it"); err != nil {
		return err
	}
	j.Logf("Erasing the old disk of osd.%d", id)
	if err := s.cephNode(ctx, m, "osd-zap", map[string]int{"id": id}, nil, 10*time.Minute); err != nil {
		j.Logf("The old disk could not be erased (%v): wipe it in Disks if it is still there", err)
	}
	if !keepID {
		s.setRolesFromOSDs(ctx)
	}
	j.Logf("osd.%d removed", id)
	return nil
}

// cephPoolUnused refuses to delete a pool or filesystem the NAS of the
// cluster still use.
func (s *Server) cephPoolUnused(ctx context.Context, pool, fs string) error {
	for _, m := range s.cephMembers() {
		var use []string
		if m.ID == s.clusterState().Self {
			s.store.Read(func(st *store.State) {
				for _, v := range st.Ceph.Volumes {
					if v.Cluster == ceph.LocalCluster && (pool != "" && v.Pool == pool || fs != "" && v.FS == fs) {
						use = append(use, v.VolumeID)
					}
				}
				for _, p := range st.Ceph.VMPools {
					if p.Cluster == ceph.LocalCluster && pool != "" && p.Pool == pool {
						use = append(use, p.Name)
					}
				}
			})
			if len(use) > 0 {
				return badRequest("this NAS still uses it: remove its Ceph volumes and VM storage first")
			}
			continue
		}
		// Other nodes only say what they use, not on which pool: refuse while
		// they use anything.
		if info, err := s.memberInfo(ctx, m); err == nil && len(info.CephUse) > 0 {
			return badRequest(m.Name + " uses storage of this Ceph cluster (" + strings.Join(info.CephUse, ", ") + "): remove it first")
		}
	}
	return nil
}

// cephClients lists the client keys (names and rights, never the keys).
func (s *Server) cephClients(ctx context.Context) []map[string]any {
	out := []map[string]any{}
	var raw struct {
		AuthDump []struct {
			Entity string            `json:"entity"`
			Caps   map[string]string `json:"caps"`
		} `json:"auth_dump"`
	}
	if err := ceph.Local.JSON(ctx, &raw, "auth", "ls"); err != nil {
		return out
	}
	for _, e := range raw.AuthDump {
		name, ok := strings.CutPrefix(e.Entity, "client.")
		if !ok || name == "admin" || strings.HasPrefix(name, "bootstrap-") {
			continue
		}
		out = append(out, map[string]any{"name": name, "caps": e.Caps})
	}
	return out
}
