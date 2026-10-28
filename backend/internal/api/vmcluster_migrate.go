package api

// Moving VMs and containers between the members of the cluster: the
// pre-checks, live migration (libvirt over TLS between members), the
// offline move of containers, maintenance of a member, and the TLS
// transport itself. A migration always runs as a job on the member that
// holds the VM; when the administrator works on another member, that member
// starts it there through the peer API and follows its progress.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/cluster"
	"strataos/internal/jobs"
	"strataos/internal/lxc"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/vmcluster"
	"strataos/internal/vms"
)

// migrateReq moves a VM of member Node to member Target.
type migrateReq struct {
	Node        string            `json:"node"`
	Name        string            `json:"name"`
	Target      string            `json:"target"`
	CopyStorage bool              `json:"copyStorage"` // accept copying local disks
	Bridges     map[string]string `json:"bridges,omitempty"`
	Storage     string            `json:"storage,omitempty"` // target folder for copied disks
}

func (q migrateReq) validate() error {
	if err := vms.ValidName(q.Name); err != nil {
		return badRequest(err.Error())
	}
	if len(q.Bridges) > 16 {
		return badRequest("too many bridges")
	}
	for k, v := range q.Bridges {
		if !validBridge(k) || !validBridge(v) {
			return badRequest("invalid bridge name")
		}
	}
	if q.Storage != "" && (!strings.HasPrefix(filepath.Clean(q.Storage), "/srv/strata/") || strings.ContainsAny(q.Storage, "\n,")) {
		return badRequest("the folder for the disks must be inside a volume")
	}
	return nil
}

func validBridge(b string) bool {
	if b == "" || len(b) > 15 {
		return false
	}
	return strings.IndexFunc(b, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-')
	}) < 0
}

// migCtx is what a checked migration carries to its execution.
type migCtx struct {
	info   *vms.MigInfo
	target store.ClusterMember
	inv    vmcInventory
	plan   vmcluster.Plan
	paths  map[string]string // copied disk → path on the target
	dir    string            // new VM folder on the target (copy)
}

func fsTypeOf(p string) string {
	out, err := sys.Run("findmnt", "-n", "-o", "FSTYPE", "-T", p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// vmcCheckHere runs the pre-checks of a migration of a VM of this member.
func (s *Server) vmcCheckHere(ctx context.Context, q migrateReq) (*migCtx, error) {
	if err := q.validate(); err != nil {
		return nil, err
	}
	c := s.clusterState()
	tm, ok := cluster.Member(c, q.Target)
	if !ok || tm.ID == c.Self {
		return nil, badRequest("choose another NAS of the cluster as the target")
	}
	mi, err := vms.MigrationInfo(q.Name)
	if err != nil {
		return nil, notFound("VM not found")
	}
	mc := &migCtx{info: mi, target: tm, paths: map[string]string{}}
	f := vmcluster.Facts{VM: mi.Name, Running: mi.State == "running" || mi.State == "paused", MemoryMB: mi.MemoryMB, CPUMode: mi.CPUMode, CPUModel: mi.CPUModel,
		HostDevs: mi.HostDevs, Snapshots: mi.Snapshots, Template: mi.Template, SrcCPU: vms.HostCPU(), BridgeMap: q.Bridges, CopyStorage: q.CopyStorage}
	for _, n := range mi.NICs {
		f.NICs = append(f.NICs, vmcluster.NICFact{Type: n.Type, Source: n.Source})
	}
	if err := s.clusterCall(ctx, tm, 20*time.Second, "vmc/inventory", nil, &mc.inv); err == nil {
		inv := mc.inv
		f.DstOnline, f.DstLibvirt, f.DstFreeMB, f.DstCPU, f.DstModels = true, inv.Libvirt, inv.FreeMB, inv.HostCPU, inv.Models
		f.DstNetworks, f.DstBridges = inv.Networks, inv.Bridges
		for _, p := range inv.Pools {
			f.DstCeph = append(f.DstCeph, p.Key)
		}
		f.DstHasVM = slices.ContainsFunc(inv.VMs, func(v vmcVM) bool { return v.Name == q.Name })
	}
	f.DstMaintenance = vmcluster.InMaintenance(s.vmcTable(), tm.ID)
	keys := map[string]string{}
	for _, p := range s.vmcPools() {
		keys[p.Secret] = p.Key
	}
	var files []string
	for _, d := range mi.Disks {
		if d.Source != "" && !strings.HasPrefix(d.Source, "rbd:") {
			files = append(files, d.Source)
		}
	}
	if len(files) > 0 && f.DstOnline {
		_ = s.clusterCall(ctx, tm, 20*time.Second, "vmc/files", map[string]any{"paths": files}, &f.DstFiles)
	}
	for _, d := range mi.Disks {
		df := vmcluster.DiskFact{Device: d.Device, Target: d.Target, Source: d.Source}
		if strings.HasPrefix(d.Source, "rbd:") {
			df.Ceph = firstNonEmpty(keys[d.Secret], "unknown")
		} else if d.Source != "" && d.Device == "disk" {
			df.Shared = fsTypeOf(d.Source) == "ceph" && slices.Contains(f.DstFiles, d.Source)
		}
		f.Disks = append(f.Disks, df)
	}
	mc.plan = vmcluster.Precheck(f)
	if mc.plan.CopyStorage {
		base := q.Storage
		if base == "" {
			base = filepath.Dir(mi.Dir)
		}
		mc.dir = filepath.Join(filepath.Clean(base), q.Name)
		for _, d := range mi.Disks {
			if slices.Contains(mc.plan.Copied, d.Target) {
				mc.paths[d.Source] = filepath.Join(mc.dir, filepath.Base(d.Source))
			}
		}
		if mi.Dir == "" {
			mc.plan.Problems = append(mc.plan.Problems, "the disks of this VM are not in a VM folder: move it with a backup")
		}
		if !slices.ContainsFunc(mc.inv.Volumes, func(v string) bool { return strings.HasPrefix(mc.dir, v+"/") }) {
			mc.plan.Problems = append(mc.plan.Problems, "the target NAS has no volume for "+mc.dir+": choose a folder of one of its volumes")
		}
	}
	return mc, nil
}

// vmcPlanMigration runs the pre-checks on the member holding the VM.
func (s *Server) vmcPlanMigration(ctx context.Context, q migrateReq) (*migCtx, vmcluster.Plan, error) {
	_, m, err := s.vmcMember(q.Node)
	if err != nil {
		return nil, vmcluster.Plan{}, err
	}
	if m.ID == s.clusterState().Self {
		mc, err := s.vmcCheckHere(ctx, q)
		if err != nil {
			return nil, vmcluster.Plan{}, err
		}
		return mc, mc.plan, nil
	}
	var p vmcluster.Plan
	err = s.vmcOnOut(ctx, m, "vmc/mig-check", q, &p, nil)
	return nil, p, err
}

// vmcPeerJobs are the jobs other members started here (and may follow).
var vmcPeerJobs sync.Map

// vmcMigrateStart starts the migration job (here, or on the member holding
// the VM, followed from here).
func (s *Server) vmcMigrateStart(r *http.Request, q migrateReq) (any, error) {
	if err := q.validate(); err != nil {
		return nil, err
	}
	c, m, err := s.vmcMember(q.Node)
	if err != nil {
		return nil, err
	}
	tm, ok := cluster.Member(c, q.Target)
	if !ok {
		return nil, badRequest("unknown target NAS")
	}
	s.audit(r, "vms", fmt.Sprintf("Migrating VM %s from %s to %s", q.Name, m.Name, tm.Name))
	title := fmt.Sprintf("Migrating VM %s to %s", q.Name, tm.Name)
	if m.ID == c.Self {
		if _, err := s.vmcCheckHere(r.Context(), q); err != nil {
			return nil, err
		}
		return s.jobs.Start("vmcluster", title, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return s.vmcMigrateHere(ctx, q, j.SetProgress, j.Logf)
		}), nil
	}
	return s.vmcRemoteJob(r, m, title, "vmc/migrate", q)
}

// vmcRemoteJob starts a job on member m and follows it in a local job.
func (s *Server) vmcRemoteJob(r *http.Request, m store.ClusterMember, title, path string, in any) (any, error) {
	var snap jobs.Snapshot
	if err := s.vmcOnOut(r.Context(), m, path, in, &snap, nil); err != nil {
		return nil, err
	}
	return s.jobs.Start("vmcluster", title, userOf(r), func(ctx context.Context, j *jobs.Job) error {
		return s.vmcFollow(ctx, m, snap.ID, j.SetProgress, j.Logf)
	}), nil
}

// vmcFollow mirrors the progress and log of a job of another member.
func (s *Server) vmcFollow(ctx context.Context, m store.ClusterMember, id string, setProgress func(float64), logf func(string, ...any)) error {
	logf("Running on %s", m.Name)
	seen, fails := 0, 0
	cancelled := false
	for {
		if ctx.Err() != nil && !cancelled {
			cancelled = true
			_ = s.clusterCall(context.Background(), m, 15*time.Second, "vmc/job-cancel", map[string]string{"id": id}, nil)
		}
		var snap jobs.Snapshot
		if err := s.clusterCall(context.Background(), m, 15*time.Second, "vmc/job", map[string]string{"id": id}, &snap); err != nil {
			if fails++; fails > 60 {
				return fmt.Errorf("%s does not answer any more: %v", m.Name, err)
			}
		} else {
			fails = 0
			if len(snap.Log) > seen {
				for _, l := range strings.Split(strings.TrimRight(snap.Log[seen:], "\n"), "\n") {
					if l != "" {
						logf("%s", l)
					}
				}
				seen = len(snap.Log)
			}
			if snap.Progress >= 0 {
				setProgress(snap.Progress)
			}
			switch snap.Status {
			case jobs.Success:
				return nil
			case jobs.Failed:
				return errors.New(firstNonEmpty(snap.Error, "failed on "+m.Name))
			}
		}
		time.Sleep(2 * time.Second)
	}
}

// vmcMigrateHere moves a VM of this member to another one.
func (s *Server) vmcMigrateHere(ctx context.Context, q migrateReq, setProgress func(float64), logf func(string, ...any)) (err error) {
	mc, err := s.vmcCheckHere(ctx, q)
	if err != nil {
		return err
	}
	if !mc.plan.OK() {
		return errors.New(strings.Join(mc.plan.Problems, "; "))
	}
	for _, w := range mc.plan.Warnings {
		logf("Note: %s", w)
	}
	tm, mi := mc.target, mc.info
	if err := s.vmcEnsureTLS(ctx); err != nil {
		return fmt.Errorf("TLS transport: %w", err)
	}
	if err := s.clusterCall(ctx, tm, 2*time.Minute, "vmc/tls", nil, nil); err != nil {
		return fmt.Errorf("TLS transport of %s: %w", tm.Name, err)
	}
	// Ceph secrets: same pool of the same cluster, this NAS's secret → the target's.
	rw := vms.Rewrite{Secrets: map[string]string{}, Bridges: q.Bridges, Paths: mc.paths}
	keys := map[string]string{}
	for _, p := range s.vmcPools() {
		keys[p.Secret] = p.Key
	}
	for _, d := range mi.Disks {
		if d.Secret == "" {
			continue
		}
		for _, p := range mc.inv.Pools {
			if p.Key == keys[d.Secret] {
				rw.Secrets[d.Secret] = p.Secret
			}
		}
	}
	// High availability: the target expects the VM (its copy is no stale one).
	ha := vmcluster.Find(s.vmcTable(), q.Name) >= 0
	setMigrating := func(to string) error {
		return s.vmcCommit(context.Background(), func(t *store.VMCluster) error {
			if i := vmcluster.Find(*t, q.Name); i >= 0 {
				t.HA[i].Migrating = to
			}
			return nil
		})
	}
	if ha {
		if err := setMigrating(tm.ID); err != nil {
			return err
		}
		defer func() {
			if err != nil {
				_ = setMigrating("")
			}
		}()
	}
	if mc.plan.CopyStorage {
		var disks []vms.DiskSpec
		for _, d := range mi.Disks {
			if p := mc.paths[d.Source]; p != "" {
				disks = append(disks, vms.DiskSpec{Path: p, Format: firstNonEmpty(d.Format, "qcow2"), Size: d.Size})
			}
		}
		logf("Creating the disks on %s in %s", tm.Name, mc.dir)
		if err := s.clusterCall(ctx, tm, 5*time.Minute, "vmc/prepare-disks", map[string]any{"dir": mc.dir, "disks": disks}, nil); err != nil {
			return err
		}
		defer func() {
			if err != nil {
				_ = s.clusterCall(context.Background(), tm, time.Minute, "vmc/discard-disks", map[string]string{"dir": mc.dir, "name": q.Name}, nil)
			}
		}()
	}
	persistent, err := vms.MigrationXML(q.Name, true, rw)
	if err != nil {
		return err
	}
	var autostart *store.VMAutostart
	s.store.Read(func(st *store.State) {
		for _, a := range st.VMs.Autostart {
			if a.Name == q.Name {
				x := a
				autostart = &x
			}
		}
	})
	if mc.plan.Live {
		active, err := vms.MigrationXML(q.Name, false, rw)
		if err != nil {
			return err
		}
		var copyDisks []string
		if mc.plan.CopyStorage {
			copyDisks = mc.plan.Copied
		}
		logf("Live migration to %s (%s)", tm.Name, tm.Addr)
		if err := vms.Migrate(ctx, q.Name, vms.MigrateOpts{Dest: firstNonEmpty(tm.Addr, tm.Host), XML: active, Persistent: persistent, CopyStorage: mc.plan.CopyStorage, CopyDisks: copyDisks}, setProgress, logf); err != nil {
			return err
		}
	} else {
		nv, _ := vms.ReadNVRAM(mi.NVRAM)
		logf("Moving the stopped VM to %s", tm.Name)
		if err := s.clusterCall(ctx, tm, 2*time.Minute, "vmc/define", map[string]any{"xml": persistent, "nvram": base64.StdEncoding.EncodeToString(nv), "nvramPath": mi.NVRAM}, nil); err != nil {
			return err
		}
		if err := vms.Undefine(q.Name); err != nil {
			return fmt.Errorf("the VM is now defined on %s but could not be removed here: %w", tm.Name, err)
		}
	}
	setProgress(100)
	// From here the VM lives on the target.
	if autostart != nil {
		_ = s.setVMAutostart(q.Name, false, 0, 0)
		if err := s.clusterCall(context.Background(), tm, 30*time.Second, "vmc/autostart", autostart, nil); err != nil {
			logf("Start with the NAS could not be set on %s: %v", tm.Name, err)
		}
	}
	if ha {
		err := s.vmcCommit(context.Background(), func(t *store.VMCluster) error {
			if i := vmcluster.Find(*t, q.Name); i >= 0 {
				t.HA[i].Owner, t.HA[i].Migrating, t.HA[i].Recover = tm.ID, "", false
				t.HA[i].Running = mc.plan.Live
				t.HA[i].XML = persistent
			}
			return nil
		})
		if err != nil {
			logf("Warning: the high availability table could not be updated: %v", err)
		}
		vmc.Lock()
		delete(vmc.seen, q.Name)
		vmc.Unlock()
	}
	if mc.plan.CopyStorage && mi.Dir != "" {
		if err := vms.RemoveDir(mi.Dir, q.Name); err != nil {
			logf("The old disks in %s could not be deleted: %v", mi.Dir, err)
		}
	}
	msg := fmt.Sprintf("VM %s migrated to %s", q.Name, tm.Name)
	s.store.Log("info", "vms", "", msg)
	logf("%s", msg)
	return nil
}

// ---- Maintenance ----

// vmcMaintenanceStart puts a member in maintenance (its VMs move to other
// members first) or takes it out.
func (s *Server) vmcMaintenanceStart(r *http.Request, m store.ClusterMember, on bool) (any, error) {
	c := s.clusterState()
	title := "Maintenance of " + m.Name
	if !on {
		title = "End of the maintenance of " + m.Name
	}
	s.audit(r, "vms", title)
	if m.ID == c.Self {
		return s.jobs.Start("vmcluster", title, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return s.vmcMaintenanceHere(ctx, on, j.SetProgress, j.Logf)
		}), nil
	}
	return s.vmcRemoteJob(r, m, title, "vmc/maintenance", map[string]bool{"on": on})
}

// vmcMaintenanceHere moves the VMs of this member away (live when they run
// and their disks are shared) and marks it in maintenance.
func (s *Server) vmcMaintenanceHere(ctx context.Context, on bool, setProgress func(float64), logf func(string, ...any)) error {
	c := s.clusterState()
	err := s.vmcCommit(ctx, func(t *store.VMCluster) error {
		t.Maintenance = slices.DeleteFunc(t.Maintenance, func(id string) bool { return id == c.Self })
		if on {
			t.Maintenance = append(t.Maintenance, c.Self)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !on {
		logf("This NAS takes VMs again")
		s.store.Log("info", "vms", "", "End of the cluster maintenance of this NAS")
		return nil
	}
	s.store.Log("info", "vms", "", "Cluster maintenance of this NAS: its VMs move to the other NAS")
	invs := s.vmcInventories(ctx)
	t := s.vmcTable()
	var nodes []vmcluster.Node
	for _, m := range c.Members {
		if inv := invs[m.ID]; inv != nil && m.ID != c.Self {
			nodes = append(nodes, vmcluster.Node{ID: m.ID, FreeMB: inv.FreeMB, OK: inv.Libvirt && !vmcluster.InMaintenance(t, m.ID)})
		}
	}
	list, _ := vms.List()
	var todo []vms.VM
	for _, vm := range list {
		ha := vmcluster.Find(t, vm.Name) >= 0
		if !vm.Template && (vm.State == "running" || ha) {
			todo = append(todo, vm)
		}
	}
	var stays []string
	for i, vm := range todo {
		pref := ""
		if k := vmcluster.Find(t, vm.Name); k >= 0 {
			pref = t.HA[k].Preferred
		}
		mem := vm.MemoryMB
		if vm.State != "running" {
			mem = 0
		}
		to := vmcluster.ChooseTarget(nodes, pref, c.Self, mem)
		if to == "" {
			stays = append(stays, vm.Name+" (no NAS with enough free memory)")
			continue
		}
		q := migrateReq{Node: c.Self, Name: vm.Name, Target: to}
		mc, err := s.vmcCheckHere(ctx, q)
		if err == nil && !mc.plan.OK() {
			err = errors.New(strings.Join(mc.plan.Problems, "; "))
		}
		if err == nil && mc.plan.CopyStorage {
			err = errors.New("its disks are on this NAS only")
		}
		if err == nil {
			base := float64(i) / float64(len(todo)) * 100
			err = s.vmcMigrateHere(ctx, q, func(p float64) { setProgress(base + p/float64(len(todo))) }, logf)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			stays = append(stays, vm.Name+" ("+err.Error()+")")
			continue
		}
		for k := range nodes {
			if nodes[k].ID == to {
				nodes[k].FreeMB -= mem
			}
		}
	}
	setProgress(100)
	if len(stays) > 0 {
		msg := "These VMs stay on this NAS and stop if it restarts: " + strings.Join(stays, ", ")
		logf("%s", msg)
		s.store.Log("warning", "vms", "", msg)
	} else {
		logf("No VM runs on this NAS any more")
	}
	return nil
}

// autoMaintenance marks a maintenance started before a restart asked by the
// Multi-NAS console: it ends by itself when the NAS is back.
const autoMaintenance = "/var/lib/strata/vmcluster/auto-maintenance"

// vmcBeforeReboot empties this member before a restart (grouped updates).
func (s *Server) vmcBeforeReboot(logf func(string, ...any)) {
	c := s.clusterState()
	if c.ID == "" || len(c.Members) < 2 || !vms.Installed() {
		return
	}
	if vmcluster.InMaintenance(s.vmcTable(), c.Self) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := s.vmcMaintenanceHere(ctx, true, func(float64) {}, logf); err != nil {
		logf("Maintenance before the restart: %v", err)
		return
	}
	_ = os.MkdirAll(filepath.Dir(autoMaintenance), 0o700)
	_ = os.WriteFile(autoMaintenance, []byte(time.Now().Format(time.RFC3339)), 0o600)
}

// vmcEndAutoMaintenance ends the maintenance started by vmcBeforeReboot.
func (s *Server) vmcEndAutoMaintenance(ctx context.Context) {
	if _, err := os.Stat(autoMaintenance); err != nil {
		return
	}
	if err := s.vmcMaintenanceHere(ctx, false, func(float64) {}, func(string, ...any) {}); err == nil {
		_ = os.Remove(autoMaintenance)
	}
}

// ---- TLS transport ----

var tlsMu sync.Mutex

// vmcEnsureTLS installs this member's certificate and trusts the members'
// authorities, and makes libvirtd listen for TLS on the cluster address.
func (s *Server) vmcEnsureTLS(ctx context.Context) error {
	tlsMu.Lock()
	defer tlsMu.Unlock()
	if !vms.Installed() {
		return errors.New("virtualization is not installed")
	}
	c := s.clusterState()
	me, ok := cluster.Member(c, c.Self)
	if !ok {
		return errors.New("this NAS is not in a cluster")
	}
	org := "strata-" + c.ID
	own, err := vmcluster.OwnCA(org)
	if err != nil {
		return err
	}
	cas := []string{own}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, m := range cluster.Others(c) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var res struct {
				CA string `json:"ca"`
			}
			if s.clusterCall(ctx, m, 10*time.Second, "vmc/ca", nil, &res) == nil && vmcluster.ValidCA(res.CA) == nil {
				mu.Lock()
				cas = append(cas, res.CA)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	var ips []string
	for _, a := range []string{me.Addr, me.PrivAddr, me.Host} {
		if a != "" && !slices.Contains(ips, a) {
			ips = append(ips, a)
		}
	}
	certChanged, err := vmcluster.EnsureCert(org, ips, vms.QemuGID())
	if err != nil {
		return err
	}
	trustChanged, err := vmcluster.SetTrust(cas)
	if err != nil {
		return err
	}
	unitChanged, err := vmcluster.WriteSocketUnit(ips)
	if err != nil {
		return err
	}
	active, _ := sys.Run("systemctl", "is-active", "libvirtd-tls.socket")
	if !certChanged && !trustChanged && !unitChanged && strings.TrimSpace(active) == "active" {
		return nil
	}
	// libvirtd takes its sockets and certificates when it starts; the VMs
	// keep running while it restarts.
	slog.Info("vm cluster: libvirt TLS configured")
	_, _ = sys.Run("systemctl", "daemon-reload")
	_, _ = sys.Run("systemctl", "stop", "libvirtd.service")
	if _, err := sys.Run("systemctl", "enable", "libvirtd-tls.socket"); err != nil {
		return err
	}
	_, _ = sys.Run("systemctl", "reset-failed", "libvirtd-tls.socket")
	if _, err := sys.Run("systemctl", "restart", "libvirtd-tls.socket"); err != nil {
		_, _ = sys.Run("systemctl", "start", "libvirtd.service")
		return err
	}
	if _, err := sys.Run("systemctl", "start", "libvirtd.service"); err != nil {
		return err
	}
	if !vms.LibvirtReady(ctx, time.Minute) {
		return errors.New("libvirt does not answer after its restart")
	}
	return nil
}

// ---- Containers ----

// lxcMigrateStart moves a container to another member (stopped meanwhile).
func (s *Server) lxcMigrateStart(r *http.Request, m store.ClusterMember, name string) (any, error) {
	var req struct {
		Target   string `json:"target"`
		Location string `json:"location"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	c := s.clusterState()
	tm, ok := cluster.Member(c, req.Target)
	if !ok || tm.ID == m.ID {
		return nil, badRequest("choose another NAS of the cluster as the target")
	}
	title := fmt.Sprintf("Moving container %s to %s", name, tm.Name)
	s.audit(r, "vms", fmt.Sprintf("Moving container %s from %s to %s", name, m.Name, tm.Name))
	in := map[string]string{"name": name, "target": req.Target, "location": req.Location}
	if m.ID == c.Self {
		return s.jobs.Start("vmcluster", title, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return s.lxcMigrateHere(ctx, name, tm, req.Location, j.SetProgress, j.Logf)
		}), nil
	}
	return s.vmcRemoteJob(r, m, title, "vmc/lxc-migrate", in)
}

// lxcMigrateHere stops a container, exports it (the backup format), sends
// the archive to the target, which imports and starts it, then deletes it
// here.
func (s *Server) lxcMigrateHere(ctx context.Context, name string, tm store.ClusterMember, location string, setProgress func(float64), logf func(string, ...any)) (err error) {
	spec, err := lxc.Load(name)
	if err != nil {
		return notFound("container not found")
	}
	var inv vmcInventory
	if err := s.clusterCall(ctx, tm, 20*time.Second, "vmc/inventory", nil, &inv); err != nil {
		return fmt.Errorf("%s does not answer: %w", tm.Name, err)
	}
	if !inv.LXC {
		return fmt.Errorf("containers (LXC) are not installed on %s", tm.Name)
	}
	if slices.ContainsFunc(inv.Containers, func(x vmcCT) bool { return x.Name == name }) {
		return fmt.Errorf("a container named %s already exists on %s", name, tm.Name)
	}
	if location == "" {
		location = spec.Storage.Location
	}
	if !slices.ContainsFunc(inv.Volumes, func(v string) bool { return location == v || strings.HasPrefix(location, v+"/") }) {
		return fmt.Errorf("%s has no volume %s: choose where to put the container", tm.Name, location)
	}
	wasRunning := lxc.State(name) == "running"
	if wasRunning {
		logf("Stopping %s", name)
		if err := lxc.Stop(name); err != nil || !lxc.WaitState(ctx, name, "stopped", 2*time.Minute) {
			_ = lxc.Kill(name)
			lxc.WaitState(ctx, name, "stopped", 30*time.Second)
		}
	}
	defer func() {
		if err != nil && wasRunning && lxc.Exists(name) {
			_ = lxc.Start(context.Background(), name)
		}
	}()
	dir := filepath.Join(spec.Storage.Location, ".strata-migrate")
	if !strings.HasPrefix(dir, "/srv/strata/") {
		dir = filepath.Join(lxc.MountRoot, filepath.Base(spec.Storage.Location), ".strata-migrate")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	logf("Exporting %s", name)
	archive, size, err := lxc.Export(ctx, name, lxc.ExportOpts{Dir: dir, Mode: "stop", AnyDir: true}, logf)
	if err != nil {
		return err
	}
	defer os.Remove(archive)
	setProgress(30)
	logf("Sending %d MB to %s", size>>20, tm.Name)
	remote, err := s.lxcSend(ctx, tm, archive, location)
	if err != nil {
		return err
	}
	setProgress(60)
	var snap jobs.Snapshot
	if err := s.clusterCall(ctx, tm, time.Minute, "vmc/lxc-import", map[string]any{"source": remote, "name": name, "location": location, "backend": spec.Storage.Backend,
		"start": wasRunning, "autostart": spec.Autostart, "order": spec.AutostartOrder, "delay": spec.AutostartDelay}, &snap); err != nil {
		return err
	}
	if err := s.vmcFollow(ctx, tm, snap.ID, func(p float64) { setProgress(60 + p*0.35) }, logf); err != nil {
		return err
	}
	logf("Deleting %s on this NAS", name)
	if err := lxc.Delete(context.Background(), name); err != nil {
		logf("The container could not be deleted here: %v", err)
	}
	s.dropContainerFromBackups(name)
	setProgress(100)
	s.store.Log("info", "vms", "", fmt.Sprintf("Container %s moved to %s", name, tm.Name))
	return nil
}

// lxcSend streams an archive to the target, which keeps it next to location.
func (s *Server) lxcSend(ctx context.Context, tm store.ClusterMember, archive, location string) (string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, _ := f.Stat()
	c := s.clusterState()
	p := cluster.PeerOf(c, tm)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, p.URL("vmc/lxc-receive?location="+url.QueryEscape(location)+"&file="+url.QueryEscape(filepath.Base(archive))), f)
	if err != nil {
		return "", err
	}
	req.ContentLength = fi.Size()
	req.Header.Set(cluster.Header, c.Token)
	req.Header.Set("X-Strata", "1")
	resp, err := p.Client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", tm.Name, strings.TrimSpace(string(data)))
	}
	return strings.TrimSpace(string(data)), nil
}

// ---- Peer side ----

func (s *Server) vmcStartPeerJob(title string, fn func(ctx context.Context, j *jobs.Job) error) jobs.Snapshot {
	snap := s.jobs.Start("vmcluster", title, "cluster", fn)
	vmcPeerJobs.Store(snap.ID, true)
	return snap
}

func (s *Server) vmcMigratePeerRoutes() {
	s.clusterPeer("POST /api/cluster/peer/vmc/mig-check", true, func(r *http.Request) (any, error) {
		var q migrateReq
		if err := decode(r, &q); err != nil {
			return nil, err
		}
		mc, err := s.vmcCheckHere(r.Context(), q)
		if err != nil {
			return nil, err
		}
		return mc.plan, nil
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/migrate", true, func(r *http.Request) (any, error) {
		var q migrateReq
		if err := decode(r, &q); err != nil {
			return nil, err
		}
		if _, err := s.vmcCheckHere(r.Context(), q); err != nil {
			return nil, err
		}
		s.store.Log("info", "vms", "", "Migration of VM "+q.Name+" asked by the cluster")
		return s.vmcStartPeerJob("Migrating VM "+q.Name, func(ctx context.Context, j *jobs.Job) error {
			return s.vmcMigrateHere(ctx, q, j.SetProgress, j.Logf)
		}), nil
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/maintenance", true, func(r *http.Request) (any, error) {
		var req struct{ On bool }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return s.vmcStartPeerJob("Cluster maintenance", func(ctx context.Context, j *jobs.Job) error {
			return s.vmcMaintenanceHere(ctx, req.On, j.SetProgress, j.Logf)
		}), nil
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/lxc-migrate", true, func(r *http.Request) (any, error) {
		var req struct{ Name, Target, Location string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := lxc.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		tm, ok := cluster.Member(s.clusterState(), req.Target)
		if !ok {
			return nil, badRequest("unknown target NAS")
		}
		return s.vmcStartPeerJob("Moving container "+req.Name, func(ctx context.Context, j *jobs.Job) error {
			return s.lxcMigrateHere(ctx, req.Name, tm, req.Location, j.SetProgress, j.Logf)
		}), nil
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/job", true, func(r *http.Request) (any, error) {
		var req struct{ ID string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if _, ok := vmcPeerJobs.Load(req.ID); !ok {
			return nil, notFound("job not found")
		}
		j, ok := s.jobs.Get(req.ID)
		if !ok {
			return nil, notFound("job not found")
		}
		return j, nil
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/job-cancel", true, func(r *http.Request) (any, error) {
		var req struct{ ID string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if _, ok := vmcPeerJobs.Load(req.ID); !ok || !s.jobs.Cancel(req.ID) {
			return nil, notFound("job not found")
		}
		return nil, nil
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/ca", true, func(r *http.Request) (any, error) {
		ca, err := vmcluster.OwnCA("strata-" + s.clusterState().ID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"ca": ca}, nil
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/tls", true, func(r *http.Request) (any, error) {
		return nil, s.vmcEnsureTLS(r.Context())
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/prepare-disks", true, func(r *http.Request) (any, error) {
		var req struct {
			Dir   string         `json:"dir"`
			Disks []vms.DiskSpec `json:"disks"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Disks) == 0 || len(req.Disks) > 16 {
			return nil, badRequest("invalid disks")
		}
		if err := vms.PrepareDisks(req.Dir, req.Disks); err != nil {
			return nil, badRequest(err.Error())
		}
		return nil, nil
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/discard-disks", true, func(r *http.Request) (any, error) {
		var req struct{ Dir, Name string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return nil, vms.DiscardDisks(req.Dir, req.Name)
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/define", true, func(r *http.Request) (any, error) {
		var req struct {
			XML       string `json:"xml"`
			NVRAM     string `json:"nvram"`
			NVRAMPath string `json:"nvramPath"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if nv, err := base64.StdEncoding.DecodeString(req.NVRAM); err == nil && len(nv) > 0 {
			if err := vms.WriteNVRAM(req.NVRAMPath, nv); err != nil {
				return nil, err
			}
		}
		if err := vms.DefineDoc(req.XML); err != nil {
			return nil, badRequest(err.Error())
		}
		return nil, nil
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/autostart", true, func(r *http.Request) (any, error) {
		var a store.VMAutostart
		if err := decode(r, &a); err != nil {
			return nil, err
		}
		return nil, s.setVMAutostart(a.Name, true, a.Order, a.Delay)
	})
	s.clusterPeerRaw("PUT /api/cluster/peer/vmc/lxc-receive", func(w http.ResponseWriter, r *http.Request) {
		loc := filepath.Clean(r.URL.Query().Get("location"))
		file := r.URL.Query().Get("file")
		if !strings.HasPrefix(loc, "/srv/strata/") || !strings.HasSuffix(file, ".tar.zst") || strings.ContainsAny(file, "/\n") || strings.HasPrefix(file, ".") {
			writeErr(w, badRequest("invalid destination"))
			return
		}
		if fi, err := os.Stat(loc); err != nil || !fi.IsDir() {
			writeErr(w, badRequest(loc+" not found"))
			return
		}
		dir := filepath.Join(loc, ".strata-migrate")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			writeErr(w, err)
			return
		}
		dst := filepath.Join(dir, file)
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			writeErr(w, badRequest(err.Error()))
			return
		}
		_, err = io.Copy(f, r.Body)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dst)
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, dst)
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/lxc-import", true, func(r *http.Request) (any, error) {
		var req struct {
			Source    string `json:"source"`
			Name      string `json:"name"`
			Location  string `json:"location"`
			Backend   string `json:"backend"`
			Start     bool   `json:"start"`
			Autostart bool   `json:"autostart"`
			Order     int    `json:"order"`
			Delay     int    `json:"delay"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		src := filepath.Clean(req.Source)
		if filepath.Base(filepath.Dir(src)) != ".strata-migrate" || !strings.HasPrefix(src, "/srv/strata/") {
			return nil, badRequest("invalid archive")
		}
		if err := lxc.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		if _, err := lxc.CheckLocation(req.Location, req.Backend); err != nil {
			req.Backend = "dir" // the volume here has another file system
		}
		return s.vmcStartPeerJob("Receiving container "+req.Name, func(ctx context.Context, j *jobs.Job) error {
			defer os.Remove(src)
			if err := lxc.Import(ctx, lxc.ImportReq{Source: src, Name: req.Name, Location: req.Location, Backend: req.Backend, Start: req.Start}, j.Logf); err != nil {
				return err
			}
			if req.Autostart {
				if sp, err := lxc.Load(req.Name); err == nil {
					set := lxc.Settings{Description: sp.Description, CPUs: sp.CPUs, MemoryMB: sp.MemoryMB, SwapMB: sp.SwapMB, SizeGB: sp.Storage.SizeGB,
						Network: sp.Network, Mounts: sp.Mounts, Features: sp.Features, Autostart: true, AutostartOrder: req.Order, AutostartDelay: req.Delay,
						UIDs: sp.IDMap.UIDs, GIDs: sp.IDMap.GIDs}
					if _, err := lxc.Update(ctx, req.Name, set); err != nil {
						j.Logf("Start with the NAS could not be set: %v", err)
					}
				}
			}
			s.store.Log("info", "vms", "", "Container "+req.Name+" received from the cluster")
			return nil
		}), nil
	})
}
