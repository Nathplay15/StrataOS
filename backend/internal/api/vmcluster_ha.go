package api

// Membership and high availability of the VM cluster. Every 5 seconds each
// member sends a heartbeat to the others (what it sees, its table version,
// its free memory). The shared table (store.VMCluster) changes only when a
// majority of the members accept the new version. The leader (lowest ID
// among the members in quorum) declares a member dead when a majority no
// longer reaches it, fences its Ceph clients and gives its HA VMs to other
// members, which start them. A member cut from the majority stops its HA
// VMs first (self-fencing), and a member that comes back removes the copies
// that now run elsewhere.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/ceph"
	"strataos/internal/cluster"
	"strataos/internal/store"
	"strataos/internal/vmcluster"
	"strataos/internal/vms"
)

// vmcBeat is a heartbeat (and its answer).
type vmcBeat struct {
	From    string   `json:"from"`
	Version int64    `json:"version"`
	Sees    []string `json:"sees"`
	FreeMB  int      `json:"freeMb"`
	Libvirt bool     `json:"libvirt"`
}

// vmc is the membership as this member sees it (memory only).
var vmc struct {
	sync.Mutex
	start     time.Time
	peers     map[string]vmcluster.Peer
	beats     map[string]vmcBeat
	quorum    bool
	leader    string
	lostSince time.Time
	ready     bool            // the table was synchronised with quorum since the start
	seen      map[string]bool // HA VMs seen running by this process
	fenced    map[string]bool // HA VMs this member stopped when it lost quorum
	booted    bool            // HA VMs to start with the NAS were handled
	commitMu  sync.Mutex
	notified  map[string]bool
	failures  map[string]int
}

type vmcState struct {
	Quorum bool
	Leader string
}

// vmcStatus returns the quorum and leader of the last round.
func vmcStatus(c store.Cluster) vmcState {
	vmc.Lock()
	defer vmc.Unlock()
	if len(c.Members) <= 1 {
		return vmcState{Quorum: true, Leader: c.Self}
	}
	return vmcState{Quorum: vmc.quorum, Leader: vmc.leader}
}

func (s *Server) vmcView(c store.Cluster) vmcluster.View {
	vmc.Lock()
	defer vmc.Unlock()
	v := vmcluster.View{Self: c.Self, Start: vmc.start, Now: time.Now(), Peers: map[string]vmcluster.Peer{}}
	for _, m := range c.Members {
		v.Members = append(v.Members, m.ID)
		if p, ok := vmc.peers[m.ID]; ok {
			v.Peers[m.ID] = p
		}
	}
	return v
}

// vmcSees lists the members this one reached recently (itself included).
func (s *Server) vmcSees(c store.Cluster) []string { return s.vmcView(c).Online() }

func (s *Server) vmcLocalBeat(c store.Cluster) vmcBeat {
	return vmcBeat{From: c.Self, Version: s.vmcTable().Version, Sees: s.vmcSees(c), FreeMB: vms.FreeMemoryMB(), Libvirt: vms.Installed()}
}

// vmcHeard records a heartbeat of a member.
func vmcHeard(b vmcBeat) {
	vmc.Lock()
	defer vmc.Unlock()
	if vmc.peers == nil {
		vmc.peers, vmc.beats = map[string]vmcluster.Peer{}, map[string]vmcBeat{}
	}
	vmc.peers[b.From] = vmcluster.Peer{LastSeen: time.Now(), Sees: b.Sees}
	vmc.beats[b.From] = b
}

// vmcStore saves a table received from a member when it is newer.
func (s *Server) vmcStore(t store.VMCluster) bool {
	accepted := false
	_ = s.store.Update(func(st *store.State) error {
		if vmcluster.Accept(st.Cluster.VMs, t) {
			st.Cluster.VMs, accepted = vmcluster.Clone(t), true
		}
		return nil
	})
	return accepted
}

// vmcCommit changes the shared table: the new version must be accepted by a
// majority of the members (this one included), or nothing changes here.
func (s *Server) vmcCommit(ctx context.Context, edit func(t *store.VMCluster) error) error {
	vmc.commitMu.Lock()
	defer vmc.commitMu.Unlock()
	c := s.clusterState()
	if c.ID == "" {
		return badRequest("this NAS is not in a cluster")
	}
	t := s.vmcTable()
	if err := edit(&t); err != nil {
		return err
	}
	vmc.Lock()
	for _, b := range vmc.beats {
		t.Version = max(t.Version, b.Version)
	}
	vmc.Unlock()
	t.Version++
	acks := 1
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, m := range cluster.Others(c) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var res struct {
				Accepted bool `json:"accepted"`
			}
			if s.clusterCall(ctx, m, 8*time.Second, "vmc/propose", map[string]any{"table": t}, &res) == nil && res.Accepted {
				mu.Lock()
				acks++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if acks < vmcluster.Majority(len(c.Members)) {
		return &Error{Status: 503, Msg: "the cluster has no quorum (too few NAS answer): the change was not saved"}
	}
	s.vmcStore(t)
	return nil
}

func (s *Server) vmcHAPeerRoutes() {
	s.clusterPeer("POST /api/cluster/peer/vmc/beat", true, func(r *http.Request) (any, error) {
		var b vmcBeat
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		c := s.clusterState()
		if _, ok := cluster.Member(c, b.From); !ok || b.From == c.Self {
			return nil, badRequest("unknown member")
		}
		vmcHeard(b)
		return s.vmcLocalBeat(c), nil
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/table", true, func(r *http.Request) (any, error) {
		return s.vmcTable(), nil
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/propose", true, func(r *http.Request) (any, error) {
		var req struct {
			Table store.VMCluster `json:"table"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := vmcValidTable(req.Table); err != nil {
			return nil, badRequest(err.Error())
		}
		return map[string]bool{"accepted": s.vmcStore(req.Table)}, nil
	})
}

func vmcValidTable(t store.VMCluster) error {
	if len(t.HA) > 500 || len(t.Maintenance) > 32 || (t.CPUModel != "" && !vmcluster.ValidModel(t.CPUModel)) {
		return errors.New("invalid VM cluster table")
	}
	for _, h := range t.HA {
		if vms.ValidName(h.Name) != nil || len(h.XML) > 256<<10 {
			return errors.New("invalid VM cluster table")
		}
		if h.XML != "" {
			if n, err := vms.DomainName(h.XML); err != nil || n != h.Name {
				return errors.New("invalid VM definition in the cluster table")
			}
		}
	}
	return nil
}

// vmcLoop runs the membership and high availability.
func (s *Server) vmcLoop() {
	vmc.Lock()
	vmc.start = time.Now()
	vmc.peers, vmc.beats = map[string]vmcluster.Peer{}, map[string]vmcBeat{}
	vmc.seen, vmc.fenced, vmc.notified, vmc.failures = map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]int{}
	vmc.Unlock()
	for round := 0; ; round++ {
		time.Sleep(vmcluster.HeartbeatEvery)
		c := s.clusterState()
		if c.ID == "" || len(c.Members) < 2 {
			vmc.Lock()
			vmc.quorum, vmc.leader, vmc.ready = true, c.Self, false
			vmc.Unlock()
			continue
		}
		func() {
			defer func() {
				if e := recover(); e != nil {
					slog.Error("vm cluster round", "panic", e)
				}
			}()
			s.vmcRound(c, round)
		}()
	}
}

func (s *Server) vmcRound(c store.Cluster, round int) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// Heartbeats, and the newest table.
	me := s.vmcLocalBeat(c)
	var wg sync.WaitGroup
	newest := store.ClusterMember{}
	var newestV int64 = me.Version
	var mu sync.Mutex
	for _, m := range cluster.Others(c) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var b vmcBeat
			if err := s.clusterCall(ctx, m, 3*time.Second, "vmc/beat", me, &b); err != nil || b.From != m.ID {
				return
			}
			vmcHeard(b)
			mu.Lock()
			if b.Version > newestV {
				newest, newestV = m, b.Version
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if newest.ID != "" {
		var t store.VMCluster
		if err := s.clusterCall(ctx, newest, 10*time.Second, "vmc/table", nil, &t); err == nil && vmcValidTable(t) == nil {
			s.vmcStore(t)
		}
	}
	v := s.vmcView(c)
	quorum := v.Quorum()
	vmc.Lock()
	vmc.quorum, vmc.leader = quorum, v.Leader()
	if quorum {
		vmc.lostSince = time.Time{}
	} else if vmc.lostSince.IsZero() {
		vmc.lostSince = time.Now()
	}
	lost := vmc.lostSince
	vmc.Unlock()
	if !vms.Installed() {
		return
	}
	t := s.vmcTable()
	if !quorum {
		if len(c.Members) >= vmcluster.MinHAMembers && vmcluster.ShouldSelfFence(v, lost) {
			s.vmcSelfFence(t, c.Self)
		}
		return
	}
	// Quorum: wait one round so the table is the cluster's.
	vmc.Lock()
	ready := vmc.ready
	vmc.ready = true
	vmc.Unlock()
	if !ready {
		return
	}
	if round%12 == 3 {
		s.vmcEndAutoMaintenance(ctx)
	}
	s.vmcRemoveStale(t, c)
	s.vmcOwnerDuties(ctx, c, round)
	if v.Leader() == c.Self {
		s.vmcRecover(ctx, c, v)
	}
	if round%120 == 1 {
		if err := s.vmcEnsureTLS(ctx); err != nil {
			slog.Debug("vm cluster TLS", "err", err)
		}
	}
}

// vmcSelfFence stops the HA VMs of a member cut from the majority: the
// others will restart them once they declare it dead.
func (s *Server) vmcSelfFence(t store.VMCluster, self string) {
	for _, h := range vmcluster.Owned(t, self) {
		st := vms.State(h.Name)
		if st == "" || st == "shut off" {
			continue
		}
		_ = vms.Action(h.Name, "destroy")
		vmc.Lock()
		vmc.fenced[h.Name] = true
		vmc.Unlock()
		msg := fmt.Sprintf("VM %s was stopped: this NAS lost contact with most of the cluster, which restarts it on another NAS", h.Name)
		s.store.Log("error", "vms", "", msg)
		s.notify("error", "VM stopped (cluster)", msg)
	}
}

// vmcRemoveStale removes the copies of HA VMs that now belong to another
// member (restarted there while this one was down or cut off).
func (s *Server) vmcRemoveStale(t store.VMCluster, c store.Cluster) {
	for _, name := range vmcluster.Stale(t, c.Self, vms.Defined()) {
		owner := t.HA[vmcluster.Find(t, name)].Owner
		on := owner
		if m, ok := cluster.Member(c, owner); ok {
			on = m.Name
		}
		running := vms.State(name) != "shut off"
		if err := vms.Undefine(name); err != nil {
			slog.Warn("stale HA VM", "vm", name, "err", err)
			continue
		}
		vmc.Lock()
		delete(vmc.seen, name)
		delete(vmc.fenced, name)
		vmc.Unlock()
		_ = s.setVMAutostart(name, false, 0, 0)
		msg := fmt.Sprintf("VM %s now runs on %s: its old copy here was removed (its disks are kept on Ceph)", name, on)
		if running {
			msg = fmt.Sprintf("VM %s now runs on %s: its old copy here was stopped and removed", name, on)
		}
		s.store.Log("warning", "vms", "", msg)
	}
}

// vmcOwnerDuties keeps the HA VMs of this member as the table says: start
// the ones given to it (recovery) or stopped by self-fencing or a restart of
// the NAS, report their state, keep their definition up to date.
func (s *Server) vmcOwnerDuties(ctx context.Context, c store.Cluster, round int) {
	t := s.vmcTable()
	var auto map[string]bool
	s.store.Read(func(st *store.State) {
		auto = map[string]bool{}
		for _, a := range st.VMs.Autostart {
			auto[a.Name] = true
		}
	})
	vmc.Lock()
	booted := vmc.booted
	vmc.booted = true
	vmc.Unlock()
	for _, h := range vmcluster.Owned(t, c.Self) {
		if h.Migrating != "" {
			continue
		}
		st := vms.State(h.Name)
		if st == "" {
			if h.XML == "" {
				continue
			}
			if err := s.vmcDefineHA(h); err != nil {
				s.vmcNotifyOnce("define-"+h.Name, "error", fmt.Sprintf("VM %s could not be defined on this NAS: %v", h.Name, err))
				continue
			}
			st = vms.State(h.Name)
		}
		running := st == "running" || st == "paused" || st == "in shutdown"
		vmc.Lock()
		seen, fenced := vmc.seen[h.Name], vmc.fenced[h.Name]
		if running {
			vmc.seen[h.Name] = true
			delete(vmc.fenced, h.Name)
		}
		vmc.Unlock()
		start := false
		switch {
		case running:
		case h.Recover:
			start = true
		case fenced && h.Running:
			start = true
		case h.Running && !seen:
			// Was running when this NAS stopped (or restarted).
			start = true
		case !booted && auto[h.Name]:
			start = true
		}
		if start {
			if err := s.vmcStartHA(ctx, h.Name); err != nil {
				// Fencing and the end of the old watch take a little while.
				vmc.Lock()
				vmc.failures[h.Name]++
				n := vmc.failures[h.Name]
				vmc.Unlock()
				if n >= 12 {
					s.vmcNotifyOnce("start-"+h.Name, "error", fmt.Sprintf("VM %s (high availability) did not start: %v", h.Name, err))
				}
				continue
			}
			running = true
			vmc.Lock()
			vmc.seen[h.Name] = true
			delete(vmc.fenced, h.Name)
			delete(vmc.notified, "start-"+h.Name)
			delete(vmc.failures, h.Name)
			vmc.Unlock()
			msg := "VM " + h.Name + " started (high availability)"
			if h.Recover {
				msg = "VM " + h.Name + " restarted on this NAS after the failure of its NAS (high availability)"
				s.notify("warning", "VM restarted (cluster)", msg)
			}
			s.store.Log("warning", "vms", "", msg)
		}
		x := ""
		if round%12 == 0 {
			if d, err := vms.InactiveXML(h.Name); err == nil && d != h.XML {
				x = d
			}
		}
		if running != h.Running || h.Recover || x != "" {
			err := s.vmcCommit(ctx, func(t *store.VMCluster) error {
				if i := vmcluster.Find(*t, h.Name); i >= 0 && t.HA[i].Owner == c.Self {
					t.HA[i].Running, t.HA[i].Recover = running, false
					if x != "" {
						t.HA[i].XML = x
					}
				}
				return nil
			})
			if err != nil {
				slog.Debug("vm cluster commit", "err", err)
			}
		}
	}
}

func (s *Server) vmcNotifyOnce(key, level, msg string) {
	vmc.Lock()
	done := vmc.notified[key]
	vmc.notified[key] = true
	vmc.Unlock()
	if !done {
		s.store.Log(level, "vms", "", msg)
		s.notify(level, "Cluster VM", msg)
	}
}

// vmcDefineHA defines here the copy of an HA VM, with the Ceph secrets of
// this NAS and without a CD-ROM image it does not have.
func (s *Server) vmcDefineHA(h store.ClusterHAVM) error {
	doc, err := vms.RewriteXML(h.XML, vms.Rewrite{Secrets: s.vmcSecretMap(h.XML), EjectMissing: true})
	if err != nil {
		return err
	}
	return vms.DefineDoc(doc)
}

// vmcSecretMap maps the Ceph secrets of a definition (another member's) to
// this member's for the same pools of the cluster's Ceph.
func (s *Server) vmcSecretMap(doc string) map[string]string {
	local := map[string]string{}
	var fsid string
	s.store.Read(func(st *store.State) {
		if st.Ceph.Local != nil {
			fsid = st.Ceph.Local.FSID
		}
		for _, p := range st.Ceph.VMPools {
			if p.Cluster == ceph.LocalCluster {
				local[p.Pool] = p.Secret
			}
		}
	})
	out := map[string]string{}
	for _, d := range vms.DomainRBDSecrets(doc) {
		if sec := local[d.Pool]; sec != "" && fsid != "" {
			out[d.Secret] = sec
		}
	}
	return out
}

// vmcStartHA starts an HA VM once its disks are free of other clients.
func (s *Server) vmcStartHA(ctx context.Context, name string) error {
	x, err := vms.InactiveXML(name)
	if err != nil {
		return err
	}
	own := cluster.LocalAddrs()
	var ips []string
	for _, a := range own {
		if i := strings.IndexByte(a, '/'); i > 0 {
			ips = append(ips, a[:i])
		}
	}
	for _, img := range vms.DomainRBD(x) {
		locks, watchers, err := rbdClients(ctx, img)
		if err != nil {
			return err
		}
		if !vmcluster.SafeToStart(locks, watchers, ips) {
			return fmt.Errorf("its disk %s is still used by another NAS (waiting for the fencing)", img)
		}
	}
	return vms.Action(name, "start")
}

// rbdClients lists the locks and watchers of an image of the cluster's Ceph.
func rbdClients(ctx context.Context, img string) ([]vmcluster.Lock, []vmcluster.Watcher, error) {
	pool, image, ok := strings.Cut(img, "/")
	if !ok || ceph.ValidPool(pool) != nil || strings.ContainsAny(image, " /@\n") || image == "" {
		return nil, nil, fmt.Errorf("invalid RBD image %s", img)
	}
	var locks []vmcluster.Lock
	out, err := ceph.Local.RBD(ctx, "lock", "ls", "--format", "json", pool+"/"+image)
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal([]byte(out), &locks); err != nil {
		// Older releases print an object keyed by lock ID.
		var m map[string]struct {
			Locker  string `json:"locker"`
			Address string `json:"address"`
		}
		if json.Unmarshal([]byte(out), &m) != nil {
			return nil, nil, fmt.Errorf("unexpected answer of rbd lock ls")
		}
		for id, l := range m {
			locks = append(locks, vmcluster.Lock{ID: id, Locker: l.Locker, Address: l.Address})
		}
	}
	var st struct {
		Watchers []vmcluster.Watcher `json:"watchers"`
	}
	out, err = ceph.Local.RBD(ctx, "status", "--format", "json", pool+"/"+image)
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return nil, nil, fmt.Errorf("unexpected answer of rbd status")
	}
	return locks, st.Watchers, nil
}

// vmcFence blocklists the Ceph clients of a dead member on the disks of a
// VM and breaks its locks, so the old QEMU can never write again.
func (s *Server) vmcFence(ctx context.Context, h store.ClusterHAVM, dead store.ClusterMember) error {
	deadIPs := slices.DeleteFunc([]string{dead.Addr, dead.PrivAddr, dead.Host}, func(x string) bool { return x == "" })
	for _, img := range vms.DomainRBD(h.XML) {
		locks, watchers, err := rbdClients(ctx, img)
		if err != nil {
			return err
		}
		for _, a := range vmcluster.FenceAddrs(locks, watchers, deadIPs) {
			// 30 days: the old process is long gone by then (the address
			// carries its nonce, so new clients of that NAS are not affected).
			if _, err := ceph.Local.Ceph(ctx, "osd", "blocklist", "add", a, "2592000"); err != nil {
				return fmt.Errorf("blocklisting %s: %w", a, err)
			}
			s.store.Log("warning", "vms", "", fmt.Sprintf("Ceph client %s of %s fenced (disk %s of VM %s)", a, dead.Name, img, h.Name))
		}
		for _, l := range vmcluster.LocksToBreak(locks, deadIPs) {
			if _, err := ceph.Local.RBD(ctx, "lock", "rm", img, l.ID, l.Locker); err != nil {
				return fmt.Errorf("releasing the lock of %s: %w", img, err)
			}
		}
	}
	return nil
}

// vmcRecover (leader) restarts on other members the HA VMs of the members
// declared dead, after fencing.
func (s *Server) vmcRecover(ctx context.Context, c store.Cluster, v vmcluster.View) {
	dead := v.Dead()
	if len(dead) == 0 {
		return
	}
	t := s.vmcTable()
	var nodes []vmcluster.Node
	vmc.Lock()
	for _, m := range c.Members {
		n := vmcluster.Node{ID: m.ID, OK: v.Reachable(m.ID)}
		if m.ID == c.Self {
			n.FreeMB, n.OK = vms.FreeMemoryMB(), true
		} else if b, ok := vmc.beats[m.ID]; ok {
			n.FreeMB = b.FreeMB
			n.OK = n.OK && b.Libvirt
		}
		nodes = append(nodes, n)
	}
	vmc.Unlock()
	plan, stuck := vmcluster.PlanRecovery(t, dead, nodes, func(h store.ClusterHAVM) int { return vms.DomainMemoryMB(h.XML) })
	for _, name := range stuck {
		s.vmcNotifyOnce("stuck-"+name, "error", fmt.Sprintf("VM %s cannot be restarted: no NAS of the cluster has enough free memory", name))
	}
	for _, p := range plan {
		h := t.HA[vmcluster.Find(t, p.VM)]
		from, _ := cluster.Member(c, p.From)
		to, _ := cluster.Member(c, p.To)
		if err := s.vmcFence(ctx, h, from); err != nil {
			s.vmcNotifyOnce("fence-"+p.VM, "error", fmt.Sprintf("VM %s is not restarted: fencing %s failed (%v)", p.VM, from.Name, err))
			continue
		}
		err := s.vmcCommit(ctx, func(t *store.VMCluster) error {
			i := vmcluster.Find(*t, p.VM)
			if i < 0 || t.HA[i].Owner != p.From {
				return errors.New("changed meanwhile")
			}
			t.HA[i].Owner, t.HA[i].Recover, t.HA[i].Migrating = p.To, true, ""
			return nil
		})
		if err != nil {
			continue
		}
		msg := fmt.Sprintf("%s does not answer: VM %s is restarted on %s (high availability)", from.Name, p.VM, to.Name)
		s.store.Log("error", "vms", "", msg)
		s.notify("error", "NAS of the cluster down", msg)
	}
}

// vmcNewCPU is the CPU model of the VMs created on a member of a cluster.
func (s *Server) vmcNewCPU() string {
	if c := s.clusterState(); c.ID == "" || len(c.Members) < 2 {
		return ""
	}
	return s.vmcTable().CPUModel
}
