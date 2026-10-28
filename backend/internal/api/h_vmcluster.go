package api

// VM cluster: the NAS of a cluster (h_cluster.go) manage their virtual
// machines and containers together. Every member shows all of them, sends
// actions and the console to the member that runs them, migrates VMs live
// between members, restarts the VMs under high availability when their
// member fails (vmcluster_ha.go) and empties a member for maintenance
// (vmcluster_migrate.go). The decisions are in internal/vmcluster.
//
//	GET    /api/vmcluster                                   members, quorum, HA, all VMs and containers
//	PUT    /api/vmcluster/settings                          {cpuModel: "" | "auto" | model}
//	POST   /api/vmcluster/nodes/{node}/vms/{name}/{action}  start | shutdown | destroy | reboot | suspend | resume
//	POST   /api/vmcluster/nodes/{node}/vms/{name}/cpu       {model} (VM shut down; "" = host CPU, "cluster" = the cluster model)
//	GET    /api/vmcluster/nodes/{node}/vms/{name}/vnc       console websocket, relayed by the member
//	POST   /api/vmcluster/nodes/{node}/lxc/{name}/{action}  start | stop | restart | kill
//	PUT    /api/vmcluster/ha/{name}                         {node, on, preferred}
//	POST   /api/vmcluster/migrate/check                     migrateReq: the pre-checks (vmcluster.Plan)
//	POST   /api/vmcluster/migrate                           migrateReq (job)
//	POST   /api/vmcluster/nodes/{node}/lxc/{name}/migrate   {target, location} (job, offline)
//	POST   /api/vmcluster/nodes/{node}/maintenance          {on} (job: the VMs move away first)

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"strataos/internal/ceph"
	"strataos/internal/cluster"
	"strataos/internal/lxc"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/vmcluster"
	"strataos/internal/vms"
)

// vmcTable returns a copy of the VM cluster table.
func (s *Server) vmcTable() store.VMCluster {
	var t store.VMCluster
	s.store.Read(func(st *store.State) { t = vmcluster.Clone(st.Cluster.VMs) })
	return t
}

// vmcPool is a libvirt RBD pool of a member, with the Ceph pool behind it.
type vmcPool struct {
	Name   string `json:"name"`
	Key    string `json:"key"` // <fsid>/<pool>
	Secret string `json:"secret"`
	Local  bool   `json:"local"` // on the cluster's own Ceph
}

func (s *Server) vmcPools() []vmcPool {
	var out []vmcPool
	s.store.Read(func(st *store.State) {
		fsid := map[string]string{}
		if st.Ceph.Local != nil {
			fsid[ceph.LocalCluster] = st.Ceph.Local.FSID
		}
		for _, e := range st.Ceph.External {
			fsid[e.ID] = e.FSID
		}
		for _, p := range st.Ceph.VMPools {
			if f := fsid[p.Cluster]; f != "" {
				out = append(out, vmcPool{Name: p.Name, Key: f + "/" + p.Pool, Secret: p.Secret, Local: p.Cluster == ceph.LocalCluster})
			}
		}
	})
	return out
}

type vmcVM struct {
	Name      string   `json:"name"`
	State     string   `json:"state"`
	VCPUs     int      `json:"vcpus"`
	MemoryMB  int      `json:"memoryMb"`
	MemUsedKB int64    `json:"memUsedKb,omitempty"`
	CPUTimeNS int64    `json:"cpuTimeNs,omitempty"`
	Storage   string   `json:"storage"` // ceph | local | mixed | none
	CPU       string   `json:"cpu"`     // host | <model>
	Template  bool     `json:"template"`
	Autostart bool     `json:"autostart"`
	IPs       []string `json:"ips,omitempty"`
	OS        string   `json:"os,omitempty"`
}

type vmcCT struct {
	Name        string   `json:"name"`
	State       string   `json:"state"`
	CPUs        float64  `json:"cpus"`
	MemoryMB    int      `json:"memoryMb"`
	MemoryBytes int64    `json:"memoryBytes,omitempty"`
	IPs         []string `json:"ips,omitempty"`
	Location    string   `json:"location"`
	Autostart   bool     `json:"autostart"`
}

// vmcInventory is what a member tells about its virtualisation.
type vmcInventory struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Libvirt    bool      `json:"libvirt"`
	LXC        bool      `json:"lxc"`
	KVM        bool      `json:"kvm"`
	MemMB      int       `json:"memMb"`
	FreeMB     int       `json:"freeMb"`
	CPUs       int       `json:"cpus"`
	HostCPU    string    `json:"hostCpu,omitempty"`
	Models     []string  `json:"models,omitempty"`
	Networks   []string  `json:"networks"`
	Bridges    []string  `json:"bridges"`
	Pools      []vmcPool `json:"pools"`
	Volumes    []string  `json:"volumes"`
	VMs        []vmcVM   `json:"vms"`
	Containers []vmcCT   `json:"containers"`
}

func (s *Server) vmcInventory() vmcInventory {
	c := s.clusterState()
	inv := vmcInventory{ID: c.Self, Name: sysHostname(), Libvirt: vms.Installed(), LXC: lxc.Installed(), KVM: vms.KVM(),
		MemMB: memTotalMB(), FreeMB: vms.FreeMemoryMB(), CPUs: runtime.NumCPU(), HostCPU: vms.HostCPU(),
		Networks: []string{}, Bridges: hostBridges(), Pools: s.vmcPools(), Volumes: []string{}, VMs: []vmcVM{}, Containers: []vmcCT{}}
	if inv.Pools == nil {
		inv.Pools = []vmcPool{}
	}
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			if v.MountPoint != "" && storage.IsMountpoint(v.MountPoint) {
				inv.Volumes = append(inv.Volumes, v.MountPoint)
			}
		}
	})
	if inv.Libvirt {
		inv.Models = vms.UsableCPUModels()
		inv.Networks = append(inv.Networks, vms.ActiveNetworks()...)
		list, _ := vms.List()
		s.decorateVMs(list)
		stats := vms.Stats()
		for _, vm := range list {
			v := vmcVM{Name: vm.Name, State: vm.State, VCPUs: vm.VCPUs, MemoryMB: vm.MemoryMB, Template: vm.Template, Autostart: vm.Autostart, OS: vm.OS, CPU: "host", Storage: "none"}
			if st, ok := stats[vm.Name]; ok {
				v.CPUTimeNS, v.MemUsedKB = st[0], st[1]
			}
			ceph, local := 0, 0
			for _, d := range vm.Disks {
				if d.Device != "disk" {
					continue
				}
				if strings.HasPrefix(d.Source, "rbd:") {
					ceph++
				} else {
					local++
				}
			}
			switch {
			case ceph > 0 && local > 0:
				v.Storage = "mixed"
			case ceph > 0:
				v.Storage = "ceph"
			case local > 0:
				v.Storage = "local"
			}
			for _, n := range vm.NICs {
				if n.IP != "" {
					v.IPs = append(v.IPs, n.IP)
				}
			}
			inv.VMs = append(inv.VMs, v)
		}
		if mi := vmCPUs(list); mi != nil {
			for i := range inv.VMs {
				if m := mi[inv.VMs[i].Name]; m != "" {
					inv.VMs[i].CPU = m
				}
			}
		}
	}
	if inv.LXC {
		for _, ct := range lxc.List() {
			inv.Containers = append(inv.Containers, vmcCT{Name: ct.Name, State: ct.State, CPUs: ct.CPUs, MemoryMB: ct.MemoryMB, MemoryBytes: ct.MemoryBytes,
				IPs: ct.IPs, Location: ct.Storage.Location, Autostart: ct.Autostart})
		}
	}
	return inv
}

// vmCPUs returns the named CPU model of each VM (absent: the host's CPU).
func vmCPUs(list []vms.VM) map[string]string {
	out := map[string]string{}
	for _, vm := range list {
		if mi, err := vms.MigrationInfo(vm.Name); err == nil && mi.CPUMode == "custom" && mi.CPUModel != "" {
			out[vm.Name] = mi.CPUModel
		}
	}
	return out
}

// vmcMember returns a member of the cluster (with this NAS's ID: itself).
func (s *Server) vmcMember(id string) (store.Cluster, store.ClusterMember, error) {
	c := s.clusterState()
	if c.ID == "" {
		return c, store.ClusterMember{}, badRequest("this NAS is not in a cluster")
	}
	m, ok := cluster.Member(c, id)
	if !ok {
		return c, m, notFound("unknown member of the cluster")
	}
	return c, m, nil
}

// vmcInventories gathers the inventory of every member (nil when offline).
func (s *Server) vmcInventories(ctx context.Context) map[string]*vmcInventory {
	c := s.clusterState()
	out := map[string]*vmcInventory{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, m := range c.Members {
		if m.ID == c.Self {
			inv := s.vmcInventory()
			out[m.ID] = &inv
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			var inv vmcInventory
			if err := s.clusterCall(ctx, m, 15*time.Second, "vmc/inventory", nil, &inv); err != nil {
				return
			}
			mu.Lock()
			out[m.ID] = &inv
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// vmAction runs a VM action here (also for a member that forwards it).
func (s *Server) vmAction(name, action string) error {
	switch action {
	case "start", "shutdown", "destroy", "reboot", "suspend", "resume":
	default:
		return notFound("unknown action")
	}
	if err := vms.Action(name, action); err != nil {
		return badRequest(err.Error())
	}
	return nil
}

func lxcAction(ctx context.Context, name, action string) error {
	var err error
	switch action {
	case "start":
		err = lxc.Start(ctx, name)
	case "stop":
		err = lxc.Stop(name)
	case "restart":
		err = lxc.Restart(ctx, name)
	case "kill":
		err = lxc.Kill(name)
	default:
		return notFound("unknown action")
	}
	if err != nil {
		return badRequest(err.Error())
	}
	return nil
}

func (s *Server) vmClusterRoutes() {
	s.admin("GET /api/vmcluster", func(r *http.Request) (any, error) {
		c := s.clusterState()
		if c.ID == "" {
			return map[string]any{"cluster": nil}, nil
		}
		t := s.vmcTable()
		invs := s.vmcInventories(r.Context())
		st := vmcStatus(c)
		type memberView struct {
			ID          string        `json:"id"`
			Name        string        `json:"name"`
			Host        string        `json:"host"`
			Self        bool          `json:"self"`
			Online      bool          `json:"online"`
			Maintenance bool          `json:"maintenance"`
			Leader      bool          `json:"leader"`
			Inventory   *vmcInventory `json:"inventory,omitempty"`
		}
		members := []memberView{}
		for _, m := range c.Members {
			members = append(members, memberView{ID: m.ID, Name: m.Name, Host: m.Host, Self: m.ID == c.Self, Online: invs[m.ID] != nil,
				Maintenance: vmcluster.InMaintenance(t, m.ID), Leader: st.Leader == m.ID, Inventory: invs[m.ID]})
		}
		type haView struct {
			Name      string `json:"name"`
			Owner     string `json:"owner"`
			Preferred string `json:"preferred,omitempty"`
			Running   bool   `json:"running"`
			Recover   bool   `json:"recover,omitempty"`
			Migrating string `json:"migrating,omitempty"`
		}
		ha := []haView{}
		for _, h := range t.HA {
			ha = append(ha, haView{h.Name, h.Owner, h.Preferred, h.Running, h.Recover, h.Migrating})
		}
		return map[string]any{"cluster": map[string]string{"id": c.ID, "name": c.Name, "self": c.Self}, "members": members, "ha": ha,
			"cpuModel": t.CPUModel, "quorum": st.Quorum, "haPossible": len(c.Members) >= vmcluster.MinHAMembers, "version": t.Version}, nil
	})

	s.admin("PUT /api/vmcluster/settings", func(r *http.Request) (any, error) {
		var req struct {
			CPUModel string `json:"cpuModel"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		model := req.CPUModel
		if model == "auto" {
			var lists [][]string
			for id, inv := range s.vmcInventories(r.Context()) {
				if inv == nil || !inv.Libvirt {
					continue
				}
				if len(inv.Models) == 0 {
					return nil, badRequest("the member " + id + " does not report its CPU models")
				}
				lists = append(lists, inv.Models)
			}
			if model = vmcluster.CommonModel(lists); model == "" {
				return nil, badRequest("the members have no CPU model in common")
			}
		} else if model != "" && !vmcluster.ValidModel(model) {
			return nil, badRequest("invalid CPU model")
		}
		if err := s.vmcCommit(r.Context(), func(t *store.VMCluster) error { t.CPUModel = model; return nil }); err != nil {
			return nil, err
		}
		s.audit(r, "vms", "Cluster CPU model of new VMs: "+firstNonEmpty(model, "host"))
		return map[string]string{"cpuModel": model}, nil
	})

	s.admin("POST /api/vmcluster/nodes/{node}/vms/{name}/{action}", func(r *http.Request) (any, error) {
		_, m, err := s.vmcMember(r.PathValue("node"))
		if err != nil {
			return nil, err
		}
		name, action := r.PathValue("name"), r.PathValue("action")
		if err := vms.ValidName(name); err != nil {
			return nil, badRequest(err.Error())
		}
		if action == "cpu" {
			var req struct {
				Model string `json:"model"`
			}
			if err := decode(r, &req); err != nil {
				return nil, err
			}
			if req.Model == "cluster" {
				if req.Model = s.vmcTable().CPUModel; req.Model == "" {
					return nil, badRequest("choose the cluster CPU model first")
				}
			}
			if req.Model != "" && !vmcluster.ValidModel(req.Model) {
				return nil, badRequest("invalid CPU model")
			}
			if err := s.vmcOn(r.Context(), m, "vmc/vm-cpu", map[string]string{"name": name, "model": req.Model}, func() error {
				if err := vms.SetCPUModel(name, req.Model); err != nil {
					return badRequest(err.Error())
				}
				return nil
			}); err != nil {
				return nil, err
			}
			s.audit(r, "vms", fmt.Sprintf("VM %s on %s: CPU %s", name, m.Name, firstNonEmpty(req.Model, "host")))
			return nil, nil
		}
		if err := s.vmcOn(r.Context(), m, "vmc/vm-action", map[string]string{"name": name, "action": action}, func() error { return s.vmAction(name, action) }); err != nil {
			return nil, err
		}
		s.audit(r, "vms", fmt.Sprintf("VM %s on %s: %s", name, m.Name, action))
		return nil, nil
	})

	s.admin("POST /api/vmcluster/nodes/{node}/lxc/{name}/{action}", func(r *http.Request) (any, error) {
		_, m, err := s.vmcMember(r.PathValue("node"))
		if err != nil {
			return nil, err
		}
		name, action := r.PathValue("name"), r.PathValue("action")
		if err := lxc.ValidName(name); err != nil {
			return nil, badRequest(err.Error())
		}
		if action == "migrate" {
			return s.lxcMigrateStart(r, m, name)
		}
		if err := s.vmcOn(r.Context(), m, "vmc/lxc-action", map[string]string{"name": name, "action": action}, func() error { return lxcAction(r.Context(), name, action) }); err != nil {
			return nil, err
		}
		s.audit(r, "vms", fmt.Sprintf("Container %s on %s: %s", name, m.Name, action))
		return nil, nil
	})

	s.adminRaw("GET /api/vmcluster/nodes/{node}/vms/{name}/vnc", func(w http.ResponseWriter, r *http.Request) {
		c, m, err := s.vmcMember(r.PathValue("node"))
		if err != nil {
			writeErr(w, err)
			return
		}
		name := r.PathValue("name")
		if err := vms.ValidName(name); err != nil {
			writeErr(w, badRequest(err.Error()))
			return
		}
		if m.ID == c.Self {
			s.vncRelay(w, r, name)
			return
		}
		s.vncProxy(w, r, c, m, name)
	})

	s.admin("PUT /api/vmcluster/ha/{name}", func(r *http.Request) (any, error) {
		var req struct {
			Node      string `json:"node"`
			On        bool   `json:"on"`
			Preferred string `json:"preferred"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name := r.PathValue("name")
		if err := vms.ValidName(name); err != nil {
			return nil, badRequest(err.Error())
		}
		c, m, err := s.vmcMember(req.Node)
		if err != nil {
			return nil, err
		}
		if req.Preferred != "" {
			if _, ok := cluster.Member(c, req.Preferred); !ok {
				return nil, badRequest("unknown preferred member")
			}
		}
		if !req.On {
			err := s.vmcCommit(r.Context(), func(t *store.VMCluster) error {
				t.HA = slices.DeleteFunc(t.HA, func(h store.ClusterHAVM) bool { return h.Name == name })
				return nil
			})
			if err != nil {
				return nil, err
			}
			s.audit(r, "vms", "High availability off for VM "+name)
			return nil, nil
		}
		if len(c.Members) < vmcluster.MinHAMembers {
			return nil, badRequest(fmt.Sprintf("high availability needs %d NAS in the cluster at least", vmcluster.MinHAMembers))
		}
		// The member that holds the VM checks it can be restarted elsewhere.
		var chk struct {
			XML     string `json:"xml"`
			Running bool   `json:"running"`
		}
		err = s.vmcOnOut(r.Context(), m, "vmc/ha-check", map[string]string{"name": name}, &chk, func() error {
			x, running, err := s.vmcHACheck(name)
			chk.XML, chk.Running = x, running
			return err
		})
		if err != nil {
			return nil, err
		}
		err = s.vmcCommit(r.Context(), func(t *store.VMCluster) error {
			if i := vmcluster.Find(*t, name); i >= 0 {
				if t.HA[i].Owner != m.ID {
					return badRequest("this VM is under high availability on another NAS")
				}
				t.HA[i].Preferred, t.HA[i].XML = req.Preferred, chk.XML
				return nil
			}
			t.HA = append(t.HA, store.ClusterHAVM{Name: name, Owner: m.ID, Preferred: req.Preferred, Running: chk.Running, XML: chk.XML})
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "vms", fmt.Sprintf("High availability on for VM %s (on %s)", name, m.Name))
		return nil, nil
	})

	s.admin("POST /api/vmcluster/migrate/check", func(r *http.Request) (any, error) {
		var req migrateReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		_, plan, err := s.vmcPlanMigration(r.Context(), req)
		if err != nil {
			return nil, err
		}
		return plan, nil
	})

	s.admin("POST /api/vmcluster/migrate", func(r *http.Request) (any, error) {
		var req migrateReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return s.vmcMigrateStart(r, req)
	})

	s.admin("POST /api/vmcluster/nodes/{node}/maintenance", func(r *http.Request) (any, error) {
		var req struct {
			On bool `json:"on"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		_, m, err := s.vmcMember(r.PathValue("node"))
		if err != nil {
			return nil, err
		}
		return s.vmcMaintenanceStart(r, m, req.On)
	})

	s.vmClusterPeerRoutes()
}

// vmcOn runs an action on member m: here, or through its peer API.
func (s *Server) vmcOn(ctx context.Context, m store.ClusterMember, path string, in any, local func() error) error {
	return s.vmcOnOut(ctx, m, path, in, nil, local)
}

func (s *Server) vmcOnOut(ctx context.Context, m store.ClusterMember, path string, in, out any, local func() error) error {
	if m.ID == s.clusterState().Self {
		return local()
	}
	if err := s.clusterCall(ctx, m, 2*time.Minute, path, in, out); err != nil {
		if pe, ok := err.(*cluster.PeerError); ok && pe.Status < 500 {
			return badRequest(pe.Msg)
		}
		return err
	}
	return nil
}

// vncRelay relays the browser's websocket to a local VM's VNC server.
func (s *Server) vncRelay(w http.ResponseWriter, r *http.Request, name string) {
	vm, err := vms.Get(name)
	if err != nil || vm.VNC <= 0 {
		writeErr(w, notFound("console unavailable (is the VM running?)"))
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	tcp, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", vm.VNC), 5*time.Second)
	if err != nil {
		c.Close(websocket.StatusInternalError, "VNC unreachable")
		return
	}
	defer tcp.Close()
	ws := websocket.NetConn(ctx, c, websocket.MessageBinary)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(ws, tcp); done <- struct{}{} }()
	go func() { _, _ = io.Copy(tcp, ws); done <- struct{}{} }()
	<-done
}

// vncProxy relays the browser's websocket to the console of a VM on another
// member, through its peer API (pinned certificate, cluster token): the
// browser keeps talking to this NAS only.
func (s *Server) vncProxy(w http.ResponseWriter, r *http.Request, c store.Cluster, m store.ClusterMember, name string) {
	p := cluster.PeerOf(c, m)
	dctx, dcancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer dcancel()
	up, _, err := websocket.Dial(dctx, strings.Replace(p.URL("vmc/vnc?name="+name), "https://", "wss://", 1), &websocket.DialOptions{
		HTTPClient: p.Client(), Subprotocols: []string{"binary"},
		HTTPHeader: http.Header{cluster.Header: {c.Token}, "X-Strata": {"1"}},
	})
	if err != nil {
		writeErr(w, &Error{Status: http.StatusBadGateway, Msg: "console of " + m.Name + " unreachable: " + err.Error()})
		return
	}
	defer up.CloseNow()
	up.SetReadLimit(-1)
	down, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		return
	}
	defer down.CloseNow()
	down.SetReadLimit(-1)
	s.audit(r, "vms", "Opened the console of "+name+" on "+m.Name)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	pipe := func(dst, src *websocket.Conn) {
		defer cancel()
		for {
			t, data, err := src.Read(ctx)
			if err != nil {
				return
			}
			if err := dst.Write(ctx, t, data); err != nil {
				return
			}
		}
	}
	go pipe(up, down)
	pipe(down, up)
}

// clusterPeerRaw registers a raw peer handler (websocket, stream),
// authenticated by the cluster token.
func (s *Server) clusterPeerRaw(pattern string, h http.HandlerFunc) {
	s.mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cluster.TokenOK(s.clusterState(), r.Header.Get(cluster.Header)) {
			s.guardFail(clientIP(r), "cluster", "")
			writeErr(w, &Error{Status: http.StatusUnauthorized, Msg: "unknown NAS"})
			return
		}
		h(w, r)
	}))
}

func (s *Server) vmClusterPeerRoutes() {
	s.clusterPeer("POST /api/cluster/peer/vmc/inventory", true, func(r *http.Request) (any, error) {
		return s.vmcInventory(), nil
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/vm-action", true, func(r *http.Request) (any, error) {
		var req struct{ Name, Action string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := vms.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		if err := s.vmAction(req.Name, req.Action); err != nil {
			return nil, err
		}
		s.store.Log("info", "vms", "", fmt.Sprintf("VM %s: %s (from the cluster)", req.Name, req.Action))
		return nil, nil
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/vm-cpu", true, func(r *http.Request) (any, error) {
		var req struct{ Name, Model string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Model != "" && !vmcluster.ValidModel(req.Model) {
			return nil, badRequest("invalid CPU model")
		}
		if err := vms.SetCPUModel(req.Name, req.Model); err != nil {
			return nil, badRequest(err.Error())
		}
		return nil, nil
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/lxc-action", true, func(r *http.Request) (any, error) {
		var req struct{ Name, Action string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := lxc.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		if err := lxcAction(r.Context(), req.Name, req.Action); err != nil {
			return nil, err
		}
		s.store.Log("info", "vms", "", fmt.Sprintf("Container %s: %s (from the cluster)", req.Name, req.Action))
		return nil, nil
	})
	s.clusterPeerRaw("GET /api/cluster/peer/vmc/vnc", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if err := vms.ValidName(name); err != nil {
			writeErr(w, badRequest(err.Error()))
			return
		}
		s.vncRelay(w, r, name)
	})
	s.clusterPeer("POST /api/cluster/peer/vmc/ha-check", true, func(r *http.Request) (any, error) {
		var req struct{ Name string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		x, running, err := s.vmcHACheck(req.Name)
		if err != nil {
			return nil, err
		}
		return map[string]any{"xml": x, "running": running}, nil
	})
	// Which of these files exist here (CD-ROM images, disks on CephFS).
	s.clusterPeer("POST /api/cluster/peer/vmc/files", true, func(r *http.Request) (any, error) {
		var req struct{ Paths []string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		out := []string{}
		for _, p := range req.Paths {
			if len(out) > 64 || !strings.HasPrefix(p, "/srv/strata/") || strings.Contains(p, "..") {
				continue
			}
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				out = append(out, p)
			}
		}
		return out, nil
	})
	s.vmcHAPeerRoutes()
	s.vmcMigratePeerRoutes()
}

// vmcHACheck checks that a local VM can be restarted on another member:
// every disk on the cluster's own Ceph (which can fence the clients of a
// dead member), nothing tied to this NAS. It returns its definition.
func (s *Server) vmcHACheck(name string) (string, bool, error) {
	mi, err := vms.MigrationInfo(name)
	if err != nil {
		return "", false, notFound("VM not found")
	}
	if mi.Template {
		return "", false, badRequest("a template cannot be under high availability")
	}
	if mi.HostDevs > 0 {
		return "", false, badRequest("a VM with USB or PCI devices passed through cannot be under high availability")
	}
	secrets := map[string]bool{}
	for _, p := range s.vmcPools() {
		if p.Local {
			secrets[p.Secret] = true
		}
	}
	disks := 0
	for _, d := range mi.Disks {
		if d.Device != "disk" {
			continue
		}
		disks++
		if !strings.HasPrefix(d.Source, "rbd:") || !secrets[d.Secret] {
			return "", false, badRequest("high availability needs every disk of the VM on the cluster's Ceph storage (disk " + d.Target + " is not)")
		}
	}
	if disks == 0 {
		return "", false, badRequest("this VM has no disk")
	}
	x, err := vms.InactiveXML(name)
	if err != nil {
		return "", false, err
	}
	return x, mi.State == "running" || mi.State == "paused", nil
}
