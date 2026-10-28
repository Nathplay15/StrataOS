package api

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"strataos/internal/backup"
	"strataos/internal/jobs"
	"strataos/internal/store"
	"strataos/internal/vms"
)

// Advanced VM management: snapshots, clones and templates, cloud images,
// imports, USB and PCI passthrough, ordered autostart, VM backups.

func autostartEntries(st *store.State) []vms.AutostartEntry {
	out := make([]vms.AutostartEntry, 0, len(st.VMs.Autostart))
	for _, a := range st.VMs.Autostart {
		out = append(out, vms.AutostartEntry{Name: a.Name, Order: a.Order, Delay: a.Delay})
	}
	return out
}

// setVMAutostart adds (with order and delay) or removes a VM from the VMs
// StrataOS starts with the NAS; libvirt's own flag is always turned off.
func (s *Server) setVMAutostart(name string, on bool, order, delay int) error {
	if err := vms.ValidName(name); err != nil {
		return err
	}
	if on && vms.IsTemplate(name) {
		return badRequest("a template cannot start with the NAS")
	}
	if order < 0 || order > 999 || delay < 0 || delay > 3600 {
		return badRequest("invalid order or delay")
	}
	vms.DisableLibvirtAutostart(name)
	return s.store.Update(func(st *store.State) error {
		list := slices.DeleteFunc(st.VMs.Autostart, func(a store.VMAutostart) bool { return a.Name == name })
		if on {
			if order == 0 {
				for _, a := range list {
					order = max(order, a.Order)
				}
				order++
			}
			list = append(list, store.VMAutostart{Name: name, Order: order, Delay: delay})
		}
		st.VMs.Autostart = list
		return nil
	})
}

// decorateVMs fills in the StrataOS autostart settings.
func (s *Server) decorateVMs(list []vms.VM) {
	s.store.Read(func(st *store.State) {
		for i := range list {
			list[i].Autostart = false
			for _, a := range st.VMs.Autostart {
				if a.Name == list[i].Name {
					list[i].Autostart, list[i].AutostartOrder, list[i].AutostartDelay = true, a.Order, a.Delay
				}
			}
		}
	})
}

// vmAutostartLoop starts the "start with the NAS" VMs once per boot, in order,
// and installs the unit that shuts them down in reverse order.
func (s *Server) vmAutostartLoop() {
	for !vms.Installed() {
		time.Sleep(time.Minute)
	}
	ctx := context.Background()
	if !vms.LibvirtReady(ctx, 5*time.Minute) {
		slog.Warn("libvirt unreachable: VMs not started")
		return
	}
	if err := vms.EnsureShutdownUnit(); err != nil {
		slog.Warn("VM shutdown unit", "err", err)
	}
	// Take over the libvirt autostart flags (VMs created before 0.6), once.
	var took bool
	s.store.Read(func(st *store.State) { took = st.VMs.TookOver })
	if list, err := vms.List(); err == nil {
		var flagged []string
		for _, vm := range list {
			if vm.LibvirtAutostart {
				flagged = append(flagged, vm.Name)
			}
		}
		_ = s.store.Update(func(st *store.State) error {
			for _, n := range flagged {
				known := slices.ContainsFunc(st.VMs.Autostart, func(a store.VMAutostart) bool { return a.Name == n })
				if !took && !known {
					st.VMs.Autostart = append(st.VMs.Autostart, store.VMAutostart{Name: n, Order: len(st.VMs.Autostart) + 1})
				}
				if !took || known {
					vms.DisableLibvirtAutostart(n)
				}
			}
			st.VMs.TookOver = true
			return nil
		})
	}
	if vms.StartedThisBoot() {
		return
	}
	vms.MarkStarted()
	var entries []vms.AutostartEntry
	s.store.Read(func(st *store.State) {
		entries = slices.DeleteFunc(autostartEntries(st), func(e vms.AutostartEntry) bool {
			// HA VMs start once the cluster says where they run.
			return slices.ContainsFunc(st.Cluster.VMs.HA, func(h store.ClusterHAVM) bool { return h.Name == e.Name })
		})
	})
	vms.StartAll(ctx, entries, func(name string, err error) {
		if err != nil {
			s.store.Log("error", "vms", "", "VM "+name+" did not start with the NAS: "+err.Error())
		} else {
			s.store.Log("info", "vms", "", "VM "+name+" started with the NAS")
		}
	})
}

// ShutdownVMs stops the ordered VMs in reverse order (strata -vm-shutdown,
// run by strata-vms.service when the NAS shuts down).
func ShutdownVMs(st *store.Store) {
	var entries []vms.AutostartEntry
	st.Read(func(s *store.State) { entries = autostartEntries(s) })
	vms.ShutdownAll(entries, 3*time.Minute, func(f string, a ...any) { slog.Info("vm shutdown", "msg", strings.TrimSpace(fmt.Sprintf(f, a...))) })
}

// adminKeys returns the SSH public keys of a user (~/.ssh/authorized_keys).
func adminKeys(name string) []string {
	keys := []string{}
	u, err := user.Lookup(name)
	if err != nil || u.HomeDir == "" {
		return keys
	}
	f, err := os.Open(filepath.Join(u.HomeDir, ".ssh", "authorized_keys"))
	if err != nil {
		return keys
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if vms.ValidSSHKey(l) {
			keys = append(keys, l)
		}
	}
	return keys
}

func (s *Server) vmProgress(j *jobs.Job) vms.Progress {
	return vms.Progress{Set: j.SetProgress, Log: j.Logf}
}

func (s *Server) vmMoreRoutes() {
	// ---- Snapshots ----
	s.admin("GET /api/vms/{name}/snapshots", func(r *http.Request) (any, error) {
		list, err := vms.Snapshots(r.PathValue("name"))
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return list, nil
	})
	s.admin("POST /api/vms/{name}/snapshots", func(r *http.Request) (any, error) {
		var req struct{ Name, Description string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name := r.PathValue("name")
		kind, err := vms.CreateSnapshot(name, req.Name, req.Description)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", "Snapshot "+req.Name+" of VM "+name+" created")
		return map[string]string{"kind": kind}, nil
	})
	s.admin("POST /api/vms/{name}/snapshots/{snap}/revert", func(r *http.Request) (any, error) {
		name, snap := r.PathValue("name"), r.PathValue("snap")
		if err := vms.RevertSnapshot(name, snap); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", "VM "+name+" reverted to snapshot "+snap)
		return nil, nil
	})
	s.admin("DELETE /api/vms/{name}/snapshots/{snap}", func(r *http.Request) (any, error) {
		name, snap := r.PathValue("name"), r.PathValue("snap")
		if err := vms.DeleteSnapshot(name, snap); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", "Snapshot "+snap+" of VM "+name+" deleted")
		return nil, nil
	})

	// ---- Clones and templates ----
	s.admin("POST /api/vms/{name}/clone", func(r *http.Request) (any, error) {
		var req vms.CloneReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		src := r.PathValue("name")
		if err := vms.ValidName(src); err != nil {
			return nil, badRequest(err.Error())
		}
		if err := vms.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", "Cloning VM "+src+" into "+req.Name)
		return s.jobs.Start("vm", "Cloning VM "+src+" into "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return vms.Clone(ctx, src, req, s.vmProgress(j))
		}), nil
	})
	s.admin("POST /api/vms/{name}/template", func(r *http.Request) (any, error) {
		var req struct {
			Template bool `json:"template"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name := r.PathValue("name")
		if err := vms.SetTemplate(name, req.Template); err != nil {
			return nil, badRequest(err.Error())
		}
		if req.Template {
			_ = s.setVMAutostart(name, false, 0, 0)
			s.audit(r, "vms", "VM "+name+" converted to a template")
		} else {
			s.audit(r, "vms", "Template "+name+" converted back to a VM")
		}
		return nil, nil
	})

	// ---- Cloud images ----
	s.admin("GET /api/vms/cloud-images", func(r *http.Request) (any, error) {
		return vms.CloudImages(), nil
	})
	s.admin("GET /api/vms/ssh-keys", func(r *http.Request) (any, error) {
		return adminKeys(userOf(r)), nil
	})
	s.admin("POST /api/vms/cloud", func(r *http.Request) (any, error) {
		var req vms.CloudReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := vms.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		if req.Cloud.Hostname == "" {
			req.Cloud.Hostname = strings.ToLower(strings.NewReplacer("_", "-", ".", "-").Replace(req.Name))
		}
		if err := req.Cloud.Validate(); err != nil {
			return nil, badRequest(err.Error())
		}
		req.CPUModel = s.vmcNewCPU()
		s.audit(r, "vms", "Creating virtual machine "+req.Name+" from cloud image "+req.Image)
		return s.jobs.Start("vm", "Creating VM "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			if err := vms.CreateFromCloud(ctx, req, s.vmProgress(j)); err != nil {
				return err
			}
			if req.Autostart {
				return s.setVMAutostart(req.Name, true, 0, 0)
			}
			return nil
		}), nil
	})
	s.admin("GET /api/vms/{name}/console-log", func(r *http.Request) (any, error) {
		log, err := vms.ConsoleLog(r.PathValue("name"))
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return map[string]string{"log": log}, nil
	})

	// ---- Import ----
	s.admin("GET /api/vms/importable", func(r *http.Request) (any, error) {
		return vms.ImportableFiles(), nil
	})
	s.admin("POST /api/vms/import/inspect", func(r *http.Request) (any, error) {
		var req struct{ Source string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		info, err := vms.InspectImport(req.Source)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return info, nil
	})
	s.admin("POST /api/vms/import", func(r *http.Request) (any, error) {
		var req vms.ImportReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := vms.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		req.CPUModel = s.vmcNewCPU()
		s.audit(r, "vms", "Importing "+req.Source+" as virtual machine "+req.Name)
		return s.jobs.Start("vm", "Importing VM "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			if err := vms.Import(ctx, req, s.vmProgress(j)); err != nil {
				return err
			}
			if req.Autostart {
				return s.setVMAutostart(req.Name, true, 0, 0)
			}
			return nil
		}), nil
	})

	// ---- USB passthrough ----
	s.admin("GET /api/vms/usb", func(r *http.Request) (any, error) {
		host := vms.HostUSB()
		if list, err := vms.List(); err == nil {
			users := vms.USBUsers(list)
			for i, u := range host {
				host[i].UsedBy = users[u.Vendor+":"+u.Product]
			}
		}
		return host, nil
	})
	s.admin("POST /api/vms/{name}/usb", func(r *http.Request) (any, error) {
		var req struct{ Vendor, Product, Serial string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name := r.PathValue("name")
		if err := vms.AttachUSB(name, req.Vendor, req.Product, req.Serial); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", "USB device "+req.Vendor+":"+req.Product+" given to VM "+name)
		return nil, nil
	})
	s.admin("DELETE /api/vms/{name}/usb/{vendor}/{product}", func(r *http.Request) (any, error) {
		name, v, p := r.PathValue("name"), r.PathValue("vendor"), r.PathValue("product")
		if err := vms.DetachUSB(name, v, p); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", "USB device "+v+":"+p+" taken back from VM "+name)
		return nil, nil
	})

	// ---- Autostart order ----
	s.admin("GET /api/vms/autostart", func(r *http.Request) (any, error) {
		var out []vms.AutostartEntry
		s.store.Read(func(st *store.State) { out = vms.StartOrder(autostartEntries(st)) })
		if out == nil {
			out = []vms.AutostartEntry{}
		}
		return out, nil
	})
	s.admin("PUT /api/vms/autostart", func(r *http.Request) (any, error) {
		var req []vms.AutostartEntry
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		list := []store.VMAutostart{}
		for _, e := range req {
			if err := vms.ValidName(e.Name); err != nil || seen[e.Name] {
				return nil, badRequest("invalid VM name (letters, digits, . - _)")
			}
			if e.Order < 0 || e.Order > 999 || e.Delay < 0 || e.Delay > 3600 {
				return nil, badRequest("invalid order or delay")
			}
			if vms.IsTemplate(e.Name) {
				return nil, badRequest("a template cannot start with the NAS")
			}
			seen[e.Name] = true
			list = append(list, store.VMAutostart{Name: e.Name, Order: e.Order, Delay: e.Delay})
			vms.DisableLibvirtAutostart(e.Name)
		}
		if err := s.store.Update(func(st *store.State) error { st.VMs.Autostart = list; return nil }); err != nil {
			return nil, err
		}
		s.audit(r, "vms", "VM start order changed")
		return nil, nil
	})

	// ---- PCI passthrough (advanced) ----
	s.admin("GET /api/vms/pci", func(r *http.Request) (any, error) {
		devs := vms.HostPCI()
		if list, err := vms.List(); err == nil {
			for _, vm := range list {
				for _, a := range vm.PCI {
					for i := range devs {
						if devs[i].Address == a {
							devs[i].UsedBy = vm.Name
						}
					}
				}
			}
		}
		return map[string]any{"iommu": vms.IOMMU(), "devices": devs}, nil
	})
	s.admin("POST /api/vms/iommu/enable", func(r *http.Request) (any, error) {
		msg, err := vms.EnableIOMMU()
		if err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", "IOMMU kernel parameters added to GRUB")
		return map[string]string{"message": msg}, nil
	})
	s.admin("POST /api/vms/{name}/pci", func(r *http.Request) (any, error) {
		var req struct{ Address string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name := r.PathValue("name")
		if err := vms.AttachPCI(name, req.Address); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", "PCI device "+req.Address+" given to VM "+name)
		return nil, nil
	})
	s.admin("DELETE /api/vms/{name}/pci/{address}", func(r *http.Request) (any, error) {
		name, a := r.PathValue("name"), r.PathValue("address")
		if err := vms.DetachPCI(name, a); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", "PCI device "+a+" taken back from VM "+name)
		return nil, nil
	})

	// ---- Backups ----
	s.admin("GET /api/vms/backups", func(r *http.Request) (any, error) {
		var tasks []store.BackupTask
		s.store.Read(func(st *store.State) {
			for _, b := range st.Backups {
				if len(b.VMs) > 0 {
					tasks = append(tasks, *b)
				}
			}
		})
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		out := []backup.VMBackup{}
		for _, t := range tasks {
			list, err := backup.VMBackups(ctx, t)
			if err != nil {
				slog.Warn("VM backups", "task", t.Name, "err", err)
				continue
			}
			out = append(out, list...)
		}
		return out, nil
	})
	s.admin("POST /api/vms/backups/restore", func(r *http.Request) (any, error) {
		var req struct {
			Task     string `json:"task"`
			VM       string `json:"vm"`
			Snapshot string `json:"snapshot"`
			vms.RestoreReq
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var t store.BackupTask
		var found bool
		s.store.Read(func(st *store.State) {
			if x := st.Backup(req.Task); x != nil {
				t, found = *x, true
			}
		})
		if !found {
			return nil, notFound("backup not found")
		}
		if err := vms.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", "Restoring VM "+req.VM+" from backup "+t.Name+" as "+req.Name)
		return s.jobs.Start("restore", "Restoring VM "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			msg, err := backup.RestoreVM(ctx, t, req.VM, req.Snapshot, req.RestoreReq, backup.Progress{Set: j.SetProgress, Log: j.Logf})
			if err == nil {
				j.Logf("%s", msg)
			}
			return err
		}), nil
	})
}
