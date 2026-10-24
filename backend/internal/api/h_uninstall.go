package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"strataos/internal/components"
	"strataos/internal/docker"
	"strataos/internal/jobs"
	"strataos/internal/lxc"
	"strataos/internal/media"
	"strataos/internal/nvr"
	"strataos/internal/remote"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/vms"
	"strataos/internal/vpn"
)

// componentMu runs one component install or uninstall at a time (apt).
var componentMu sync.Mutex

// componentPlan is what uninstalling a component does, shown before the
// administrator confirms and computed again by the job.
type componentPlan struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	Installed bool                 `json:"installed"`
	Blockers  []components.Blocker `json:"blockers"`
	Packages  []string             `json:"packages"` // purged
	Orphans   []string             `json:"orphans"`  // pulled in by them, unused afterwards
	Kept      []components.Kept    `json:"kept"`
	Services  []string             `json:"services"` // stopped and disabled
	Removed   []string             `json:"removed"`  // configuration, caches, APT source
	Data      []string             `json:"data"`     // deleted only on request
	Effects   []string             `json:"effects"`  // settings turned off
	Blocked   bool                 `json:"blocked"`  // refused even with force
	inUse     []string
	facts     components.Facts
	component *components.Component
}

// aptEnv makes apt non-interactive and patient with a running update.
var aptEnv = []string{"DEBIAN_FRONTEND=noninteractive"}

func aptArgs(args ...string) []string {
	return append([]string{"-o", "DPkg::Lock::Timeout=300"}, args...)
}

// installedPackages returns which of pkgs dpkg reports as installed.
func installedPackages(pkgs []string) map[string]bool {
	out := map[string]bool{}
	if len(pkgs) == 0 {
		return out
	}
	// Unknown packages make dpkg-query fail but the others are still printed.
	res, _ := sys.RunOpts(context.Background(), sys.Opts{AllowCodes: []int{-2}}, "dpkg-query", append([]string{"-W", "-f", "${Package} ${db:Status-Abbrev}\n"}, pkgs...)...)
	for _, l := range strings.Split(res, "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && strings.HasPrefix(f[1], "ii") {
			name, _, _ := strings.Cut(f[0], ":")
			out[name] = true
		}
	}
	return out
}

// aptSimulate returns what an apt-get command would remove (args after -s).
func aptSimulate(ctx context.Context, args ...string) ([]string, error) {
	out, err := sys.RunOpts(ctx, sys.Opts{Env: aptEnv, Timeout: 2 * time.Minute}, "apt-get", append([]string{"-s"}, args...)...)
	if err != nil {
		return nil, err
	}
	return components.ParseAptSim(out), nil
}

func existing(globs []string) []string {
	var out []string
	for _, g := range globs {
		m, _ := filepath.Glob(g)
		for _, p := range m {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// componentFacts gathers what the blockers of a component depend on.
func (s *Server) componentFacts(ctx context.Context, id string) components.Facts {
	var f components.Facts
	s.store.Read(func(st *store.State) {
		for _, t := range st.ISCSI.Targets {
			f.Targets = append(f.Targets, t.Name)
		}
		f.HARole = st.HA.Role
		f.Directory = st.Directory.Type
		f.VPNServer = st.VPN.Enabled
		f.VPNDevices = len(st.VPN.Peers)
		for _, l := range st.VPN.Links {
			if !l.Disabled {
				f.VPNLinks = append(f.VPNLinks, l.Name)
			}
		}
		f.VPNClient = vpn.ClientActive(st.VPN.Client)
		if st.UPS != nil {
			f.UPSMode = st.UPS.Mode
		}
		f.DLNA = st.Media.DLNA.Enabled
		f.FTP = st.Files.FTP
		for _, c := range st.NVR.Cameras {
			if c.Enabled && c.Engine == "" {
				f.Cameras = append(f.Cameras, c.Name)
			}
		}
		for _, c := range st.Clouds {
			if c.Enabled {
				f.Syncs = append(f.Syncs, c.Name)
			}
		}
		for _, b := range st.Backups {
			if b.Enabled && b.Kind == "sync" {
				f.Syncs = append(f.Syncs, b.Name)
			}
		}
		for _, sc := range st.Schedules {
			if sc.Enabled && sc.Type == "antivirus" {
				f.AVSchedules = append(f.AVSchedules, sc.Name)
			}
		}
		f.Tunnel = st.Remote.Tunnel.Enabled
	})
	// The slower checks only for the component concerned.
	switch id {
	case "zfs":
		pools, _ := storage.ListZPools(false)
		for _, p := range pools {
			f.Pools = append(f.Pools, p.Name)
		}
	case "docker":
		f.Stacks = docker.StackNames()
		if docker.Available() {
			var list []struct {
				Names  []string          `json:"Names"`
				Labels map[string]string `json:"Labels"`
			}
			_ = s.docker.Call(ctx, "GET", "/containers/json", map[string][]string{"all": {"1"}}, nil, &list)
			for _, c := range list {
				if p := c.Labels["com.docker.compose.project"]; p != "" && slices.Contains(f.Stacks, p) {
					continue
				}
				if len(c.Names) > 0 {
					f.Containers = append(f.Containers, strings.TrimPrefix(c.Names[0], "/"))
				}
			}
		}
	case "vms":
		if vms.Installed() {
			list, _ := vms.List()
			for _, v := range list {
				f.VMs = append(f.VMs, v.Name)
			}
		}
	case "lxc":
		f.LXC = lxc.Names()
	case "tailscale":
		if remote.TailscaleInstalled() {
			f.Tailscale = remote.TailscaleStatus(ctx).LoggedIn
		}
	}
	return f
}

// componentsInUse lists the installed components that keep their packages.
// The recorder only needs ffmpeg while it has cameras: ffmpeg alone cannot
// tell whether it was installed for it.
func (s *Server) componentsInUse(except string) []string {
	var cameras bool
	s.store.Read(func(st *store.State) {
		cameras = slices.ContainsFunc(st.NVR.Cameras, func(c *store.Camera) bool { return c.Engine == "" })
	})
	var out []string
	for id, ok := range componentsInstalled() {
		if !ok || id == except || (id == "nvr" && !cameras) {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func strataDepends() string {
	out, _ := sys.Run("dpkg-query", "-W", "-f", "${Depends}", "strataos")
	return out
}

// componentPlan computes what uninstalling component id would do.
func (s *Server) componentPlan(ctx context.Context, id string) (*componentPlan, error) {
	c := components.Find(id)
	if c == nil {
		return nil, notFound("this component cannot be uninstalled")
	}
	p := &componentPlan{ID: id, Name: c.Name, component: c, Blockers: []components.Blocker{}, Packages: []string{}, Orphans: []string{}, Kept: []components.Kept{}, Services: []string{}, Removed: []string{}, Data: []string{}, Effects: []string{}}
	p.facts = s.componentFacts(ctx, id)
	p.Blockers = append(p.Blockers, components.Blockers(id, p.facts)...)
	p.Blocked = components.Refused(p.Blockers, true)
	p.inUse = s.componentsInUse(id)
	present := installedPackages(c.Packages)
	protected := components.Protected(strataDepends())
	remove, kept := components.Packages(id, func(pkg string) bool { return present[pkg] }, p.inUse, protected)
	p.Kept = append(p.Kept, kept...)
	// The purge must not take anything else with it (strataos, samba…): a
	// package whose removal would is kept.
	if len(remove) > 0 {
		for range 3 {
			got, err := aptSimulate(ctx, append(aptArgs("purge"), remove...)...)
			if err != nil {
				return nil, fmt.Errorf("apt simulation failed: %w", err)
			}
			if len(components.Without(got, remove)) == 0 {
				break
			}
			var next []string
			for _, pkg := range remove {
				one, _ := aptSimulate(ctx, aptArgs("purge", pkg)...)
				if extra := components.Without(one, remove); len(extra) > 0 {
					if len(extra) > 3 {
						extra = append(extra[:3], "…")
					}
					p.Kept = append(p.Kept, components.Kept{Package: pkg, Reason: "Needed by " + strings.Join(extra, ", ")})
					continue
				}
				next = append(next, pkg)
			}
			remove = next
			if len(remove) == 0 {
				break
			}
		}
		if len(remove) > 0 {
			got, err := aptSimulate(ctx, append(aptArgs("purge"), remove...)...)
			if err != nil {
				return nil, fmt.Errorf("apt simulation failed: %w", err)
			}
			if extra := components.Without(got, remove); len(extra) > 0 {
				return nil, fmt.Errorf("removing %s would also remove %s", strings.Join(remove, " "), strings.Join(extra, " "))
			}
		}
	}
	p.Packages = append(p.Packages, remove...)
	// Shared packages alone (nftables…) do not make a component installed.
	p.Installed = componentsInstalled()[id] || len(remove) > 0
	if len(remove) > 0 {
		before, _ := aptSimulate(ctx, aptArgs("autoremove")...)
		after, _ := aptSimulate(ctx, append(aptArgs("purge", "--autoremove"), remove...)...)
		p.Orphans = append(p.Orphans, s.orphansToRemove(components.Without(components.Without(after, remove), before), p.inUse, protected)...)
	}
	for _, u := range append(slices.Clone(c.Services), c.Units...) {
		if unitExists(u) {
			p.Services = append(p.Services, u)
		}
	}
	p.Removed = append(p.Removed, existing(append(append(slices.Clone(c.Files), c.Caches...), c.Repo...))...)
	for _, u := range c.Units {
		if path := "/etc/systemd/system/" + u; fileExists(path) && !slices.Contains(p.Removed, path) {
			p.Removed = append(p.Removed, path)
		}
	}
	p.Data = append(p.Data, existing(c.Data)...)
	p.Data = append(p.Data, s.componentStateData(id)...)
	p.Effects = append(p.Effects, componentEffects(id, p.facts)...)
	return p, nil
}

// orphansToRemove drops from the packages left unused those StrataOS or an
// installed component needs.
func (s *Server) orphansToRemove(orphans, inUse []string, protected map[string]string) []string {
	needed := map[string]bool{}
	for _, c := range components.All {
		if slices.Contains(inUse, c.ID) {
			for _, p := range c.Packages {
				needed[p] = true
			}
		}
	}
	out := []string{}
	for _, p := range orphans {
		if protected[p] == "" && !needed[p] && p != "strataos" {
			out = append(out, p)
		}
	}
	return out
}

func unitExists(u string) bool {
	out, _ := sys.RunOpts(context.Background(), sys.Opts{AllowCodes: []int{-2}}, "systemctl", "list-unit-files", "--no-legend", u)
	return strings.TrimSpace(out) != ""
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// componentStateData lists the data StrataOS keeps for a component in its
// own state (and the recordings of the cameras): deleted only on request.
func (s *Server) componentStateData(id string) []string {
	var out []string
	s.store.Read(func(st *store.State) {
		switch id {
		case "vpn":
			if len(st.VPN.Peers) > 0 || len(st.VPN.Links) > 0 || st.VPN.PrivateKey != "" || st.VPN.Client.PrivateKey != "" {
				out = append(out, fmt.Sprintf("VPN keys, %d device(s) and %d site-to-site link(s)", len(st.VPN.Peers), len(st.VPN.Links)))
			}
		case "tunnel":
			if st.Remote.Tunnel.Token != "" {
				out = append(out, "Cloudflare Tunnel token")
			}
		case "nvr":
			for _, c := range st.NVR.Cameras {
				if c.Engine == "" {
					if dir := camFolder(st.NVR, c); dir != "" && fileExists(dir) {
						out = append(out, dir)
					}
				}
			}
		}
	})
	return out
}

// componentEffects says which settings are turned off.
func componentEffects(id string, f components.Facts) []string {
	switch id {
	case "ftp":
		return []string{"The FTP server is turned off"}
	case "dlna":
		return []string{"The DLNA server is turned off"}
	case "nut":
		return []string{"The UPS configuration is removed"}
	case "vpn":
		return []string{"The VPN server, the site-to-site links and the outgoing VPN are turned off"}
	case "tunnel":
		return []string{"The Cloudflare Tunnel is turned off"}
	case "tailscale":
		if f.Tailscale {
			return []string{"The NAS is signed out of Tailscale"}
		}
	case "rclone":
		if len(f.Syncs) > 0 {
			return []string{"Cloud syncs and two-way syncs are disabled"}
		}
	case "antivirus":
		if len(f.AVSchedules) > 0 {
			return []string{"Scheduled antivirus scans are disabled"}
		}
	case "nvr":
		if len(f.Cameras) > 0 {
			return []string{"Cameras of the built-in recorder are disabled"}
		}
	case "iscsi":
		return []string{"The iSCSI service is turned off"}
	case "photos":
		return []string{"Photos thumbnails and the playback of other video formats in Files need the media tools again"}
	case "docpreview":
		return []string{"Office documents are no longer shown in Files"}
	}
	return nil
}

// componentOff turns StrataOS's own use of a component off before its
// packages go, and deletes the data it keeps in its state when asked.
func (s *Server) componentOff(ctx context.Context, j *jobs.Job, p *componentPlan, deleteData bool) error {
	update := func(fn func(st *store.State)) error {
		return s.store.Update(func(st *store.State) error { fn(st); return nil })
	}
	var err error
	switch p.ID {
	case "nut":
		err = update(func(st *store.State) { st.UPS = nil })
	case "vms":
		err = update(func(st *store.State) { st.VMs = store.VMSettings{} })
	case "lxc":
		err = update(func(st *store.State) {
			for _, b := range st.Backups {
				b.Containers = nil
			}
		})
	case "rclone":
		err = update(func(st *store.State) {
			for _, c := range st.Clouds {
				c.Enabled = false
			}
			for _, b := range st.Backups {
				if b.Kind == "sync" {
					b.Enabled = false
				}
			}
		})
	case "ftp":
		if err = update(func(st *store.State) { st.Files.FTP = false }); err == nil {
			err = s.applyFileServices()
		}
	case "antivirus":
		err = update(func(st *store.State) {
			for _, sc := range st.Schedules {
				if sc.Type == "antivirus" {
					sc.Enabled = false
				}
			}
		})
	case "vpn":
		err = update(func(st *store.State) {
			st.VPN.Enabled = false
			st.VPN.Client.Enabled = false
			for i := range st.VPN.Links {
				st.VPN.Links[i].Disabled = true
			}
			if deleteData {
				st.VPN.Peers, st.VPN.Links = nil, nil
				st.VPN.PrivateKey, st.VPN.PublicKey = "", ""
				st.VPN.Client = store.VPNClient{}
			}
		})
		if err == nil {
			err = s.applyVPN()
		}
	case "tailscale":
		if p.facts.Tailscale {
			j.Logf("Signing out of Tailscale...")
			if e := remote.TailscaleLogout(); e != nil {
				j.Logf("Sign-out failed: %v", e)
			}
		}
		if deleteData {
			err = update(func(st *store.State) { st.Remote.Tailscale = store.Tailscale{} })
		}
	case "tunnel":
		err = update(func(st *store.State) {
			st.Remote.Tunnel.Enabled = false
			if deleteData {
				st.Remote.Tunnel = store.CloudflareTunnel{}
			}
		})
		if err == nil {
			err = remote.ApplyTunnel(store.CloudflareTunnel{})
		}
	case "iscsi":
		err = update(func(st *store.State) { st.ISCSI.Enabled = false })
	case "dlna":
		if err = update(func(st *store.State) { st.Media.DLNA.Enabled = false }); err == nil {
			err = media.ApplyDLNA(false, media.DLNAConfig{})
		}
	case "nvr":
		var dirs []string
		err = update(func(st *store.State) {
			for _, c := range st.NVR.Cameras {
				if c.Engine == "" {
					c.Enabled = false
					if d := camFolder(st.NVR, c); d != "" {
						dirs = append(dirs, d)
					}
				}
			}
		})
		s.nvrSync()
		if deleteData {
			for _, d := range dirs {
				j.Logf("Deleting %s", d)
				_ = os.RemoveAll(d)
			}
		}
	}
	return err
}

// uninstallComponent is the job.
func (s *Server) uninstallComponent(ctx context.Context, j *jobs.Job, id string, force, deleteData bool, user string) error {
	componentMu.Lock()
	defer componentMu.Unlock()
	p, err := s.componentPlan(ctx, id)
	if err != nil {
		return err
	}
	if components.Refused(p.Blockers, force) {
		return errors.New(blockersText(p))
	}
	c := p.component
	j.Logf(">> Turning off %s in StrataOS", c.Name)
	if err := s.componentOff(ctx, j, p, deleteData); err != nil {
		j.Logf("warning: %v", err)
	}
	j.SetProgress(10)
	for _, u := range p.Services {
		j.Logf("Stopping %s", u)
		_, _ = sys.RunOpts(ctx, sys.Opts{AllowCodes: []int{-2}}, "systemctl", "disable", "--now", u)
	}
	for _, u := range c.Units {
		_ = os.Remove("/etc/systemd/system/" + u)
	}
	_, _ = sys.Run("systemctl", "daemon-reload")
	j.SetProgress(20)
	if len(p.Packages) > 0 {
		before, _ := aptSimulate(ctx, aptArgs("autoremove")...)
		j.Logf(">> Removing %s", strings.Join(p.Packages, " "))
		if _, err := sys.RunOpts(ctx, sys.Opts{Output: j, Env: aptEnv, Timeout: 30 * time.Minute}, "apt-get", append(aptArgs("purge", "-y"), p.Packages...)...); err != nil {
			return err
		}
		after, _ := aptSimulate(ctx, aptArgs("autoremove")...)
		orphans := s.orphansToRemove(components.Without(after, before), p.inUse, components.Protected(strataDepends()))
		if len(orphans) > 0 {
			j.Logf(">> Removing what they pulled in: %s", strings.Join(orphans, " "))
			if _, err := sys.RunOpts(ctx, sys.Opts{Output: j, Env: aptEnv, Timeout: 30 * time.Minute}, "apt-get", append(aptArgs("purge", "-y"), orphans...)...); err != nil {
				j.Logf("warning: %v", err)
			}
		}
	}
	for _, k := range p.Kept {
		j.Logf("Kept %s (%s)", k.Package, k.Reason)
	}
	j.SetProgress(80)
	for _, f := range existing(append(append(slices.Clone(c.Files), c.Caches...), c.Repo...)) {
		j.Logf("Removing %s", f)
		_ = os.RemoveAll(f)
	}
	if deleteData {
		for _, d := range existing(c.Data) {
			j.Logf("Deleting %s", d)
			_ = os.RemoveAll(d)
		}
	}
	if id == "docpreview" {
		_, _ = sys.RunOpts(ctx, sys.Opts{AllowCodes: []int{-2}}, "userdel", "strata-preview")
	}
	_, _ = sys.Run("systemctl", "daemon-reload")
	_, _ = sys.Run("systemctl", "reset-failed")
	msg := fmt.Sprintf("Component %s uninstalled", c.Name)
	if deleteData {
		msg = fmt.Sprintf("Component %s uninstalled with its data", c.Name)
	}
	s.store.Log("info", "system", user, msg)
	j.Logf(">> %s", msg)
	return nil
}

func blockersText(p *componentPlan) string {
	var m []string
	for _, b := range p.Blockers {
		m = append(m, b.Message)
	}
	return fmt.Sprintf("%s cannot be uninstalled: %s", p.Name, strings.Join(m, "; "))
}

// ---- App Center apps ----

// appPlan is what uninstalling an app (catalog or custom) does.
type appPlan struct {
	Stack        string                `json:"stack"`
	App          string                `json:"app,omitempty"` // catalog id
	Name         string                `json:"name"`
	Containers   int                   `json:"containers"`
	Volumes      []string              `json:"volumes"` // named Docker volumes (data)
	Folders      []string              `json:"folders"` // app folders (data)
	Kept         []components.KeptPath `json:"kept"`    // mounted folders never deleted
	Images       []string              `json:"images"`
	Integrations []string              `json:"integrations"` // StrataOS settings cleaned
}

func (s *Server) appPlan(ctx context.Context, stack string) (*appPlan, error) {
	if err := docker.ValidStack(stack); err != nil {
		return nil, badRequest(err.Error())
	}
	stacks, _ := docker.ListStacks(ctx, s.docker)
	i := slices.IndexFunc(stacks, func(x docker.Stack) bool { return x.Name == stack })
	if i < 0 {
		return nil, notFound("no app or stack named " + stack)
	}
	st := stacks[i]
	p := &appPlan{Stack: stack, App: st.App, Name: stack, Containers: st.Services, Volumes: []string{}, Folders: []string{}, Kept: []components.KeptPath{}, Images: []string{}, Integrations: []string{}}
	if a := docker.FindApp(st.App); a != nil {
		p.Name = a.Name
	} else if st.Custom != nil {
		p.Name = st.Custom.Name
	}
	binds, images, _ := docker.StackMounts(ctx, stack)
	others := map[string][]string{}
	for _, o := range stacks {
		if o.Name != stack {
			others[o.Name], _, _ = docker.StackMounts(ctx, o.Name)
		}
	}
	shares := map[string][]string{}
	for name, path := range s.sharePaths() {
		shares[name] = []string{path}
	}
	del, kept := components.AppData(st.App, stack, binds, others, shares)
	for _, d := range del {
		// Only real folders: a symbolic link could lead out of the app folder.
		if real, err := filepath.EvalSymlinks(d); err == nil && real == d {
			p.Folders = append(p.Folders, d)
		}
	}
	p.Kept = append(p.Kept, kept...)
	p.Images = append(p.Images, images...)
	p.Volumes = append(p.Volumes, docker.StackVolumes(ctx, stack)...)
	p.Integrations = append(p.Integrations, s.appIntegrations(st.App)...)
	return p, nil
}

// appIntegrations lists the StrataOS settings tied to an app.
func (s *Server) appIntegrations(app string) []string {
	var out []string
	s.store.Read(func(st *store.State) {
		switch app {
		case docker.OfficeApp:
			if st.Office.Enabled && st.Office.ServerURL == "" {
				out = append(out, "Editing office documents with OnlyOffice is turned off")
			}
		case "frigate":
			if slices.ContainsFunc(st.NVR.Cameras, func(c *store.Camera) bool { return c.Engine == "frigate" }) {
				out = append(out, "Cameras recorded by Frigate go back to the built-in recorder, disabled")
			}
		case media.OwnToneApp:
			if len(st.Media.Music.Shares) > 0 || st.Media.Music.Users {
				out = append(out, "The music folders of the music server are cleared")
			}
		}
	})
	return out
}

// appCleanup clears the StrataOS settings of an app that was removed.
func (s *Server) appCleanup(app string, out io.Writer) {
	switch app {
	case docker.OfficeApp:
		_ = s.store.Update(func(st *store.State) error {
			if st.Office.ServerURL == "" && st.Office.Enabled {
				st.Office.Enabled = false
				fmt.Fprintln(out, "OnlyOffice editing turned off")
			}
			return nil
		})
	case "frigate":
		_ = s.store.Update(func(st *store.State) error {
			for _, c := range st.NVR.Cameras {
				if c.Engine == "frigate" {
					c.Engine, c.Enabled = "", false
					fmt.Fprintf(out, "Camera %s back to the built-in recorder (disabled)\n", c.Name)
				}
			}
			return nil
		})
		_ = os.RemoveAll(nvr.FrigateSecretsDir)
		s.nvrSync()
	case media.OwnToneApp:
		_ = s.store.Update(func(st *store.State) error {
			st.Media.Music = store.MusicServer{}
			return nil
		})
		fmt.Fprintln(out, "Music server settings cleared")
	}
}

func (s *Server) uninstallRoutes() {
	s.admin("GET /api/system/components/{name}/uninstall", func(r *http.Request) (any, error) {
		return s.componentPlan(r.Context(), r.PathValue("name"))
	})
	s.admin("POST /api/system/components/{name}/uninstall", func(r *http.Request) (any, error) {
		var req struct {
			Force      bool `json:"force"`
			DeleteData bool `json:"deleteData"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		id := r.PathValue("name")
		p, err := s.componentPlan(r.Context(), id)
		if err != nil {
			return nil, err
		}
		if !p.Installed {
			return nil, badRequest(p.Name + " is not installed")
		}
		if components.Refused(p.Blockers, req.Force) {
			return nil, &Error{Status: http.StatusConflict, Msg: blockersText(p)}
		}
		s.audit(r, "system", "Uninstalling component "+id)
		user := userOf(r)
		return s.jobs.Start("uninstall", "Uninstall "+p.Name, user, func(ctx context.Context, j *jobs.Job) error {
			return s.uninstallComponent(ctx, j, id, req.Force, req.DeleteData, user)
		}), nil
	})

	s.admin("GET /api/apps/{stack}/uninstall", func(r *http.Request) (any, error) {
		return s.appPlan(r.Context(), r.PathValue("stack"))
	})
	s.admin("POST /api/apps/{stack}/uninstall", func(r *http.Request) (any, error) {
		var req struct {
			DeleteData   bool `json:"deleteData"`
			RemoveImages bool `json:"removeImages"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		p, err := s.appPlan(r.Context(), r.PathValue("stack"))
		if err != nil {
			return nil, err
		}
		s.audit(r, "apps", "Uninstalling "+p.Name+" ("+p.Stack+")")
		user := userOf(r)
		return s.jobs.Start("app", "Uninstall "+p.Name, user, func(ctx context.Context, j *jobs.Job) error {
			j.Logf(">> Stopping and removing the containers and networks of %s", p.Stack)
			if err := docker.DeleteStack(ctx, p.Stack, req.DeleteData); err != nil {
				return err
			}
			if req.DeleteData {
				for _, v := range p.Volumes {
					j.Logf("Docker volume %s deleted", v)
				}
				for _, d := range p.Folders {
					j.Logf("Deleting %s", d)
					if err := os.RemoveAll(d); err != nil {
						j.Logf("warning: %v", err)
					}
				}
			}
			if req.RemoveImages {
				docker.RemoveImages(ctx, p.Images, j)
			}
			s.appCleanup(p.App, j)
			msg := fmt.Sprintf("App %s uninstalled", p.Name)
			if req.DeleteData {
				msg = fmt.Sprintf("App %s uninstalled with its data", p.Name)
			}
			s.store.Log("info", "apps", user, msg)
			j.Logf(">> %s", msg)
			return nil
		}), nil
	})
}
