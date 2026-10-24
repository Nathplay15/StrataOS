package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"

	"strataos/internal/jobs"
	"strataos/internal/lxc"
	"strataos/internal/storage"
	"strataos/internal/store"
)

// LXC system containers, part of the "Virtual machines" app. Every route is
// for administrators and lives under /api/lxc (also used by the Android app):
//
//	GET    /api/lxc                          list: {installed, containers, bridges, natBridge, arch, cores}
//	GET    /api/lxc/templates[?refresh=1]    images of images.linuxcontainers.org for this NAS
//	GET    /api/lxc/storage                  locations for root file systems and their backends
//	GET    /api/lxc/importable               backups (.tar.zst) and container folders that can be imported
//	POST   /api/lxc                          create (job): lxc.CreateReq + mapUsers, mapGroups
//	POST   /api/lxc/import                   import a backup or adopt a folder (job): lxc.ImportReq
//	GET    /api/lxc/{name}                   one container (with live state)
//	PUT    /api/lxc/{name}                   change settings (lxc.Settings + mapUsers, mapGroups): {restart}
//	DELETE /api/lxc/{name}                   delete a stopped container and its data
//	POST   /api/lxc/{name}/{action}          start | stop | restart | kill
//	POST   /api/lxc/{name}/clone             {name} (job)
//	POST   /api/lxc/{name}/rename            {name}
//	GET    /api/lxc/{name}/stats[?disk=1]    resource counters (lxc.Stats)
//	POST   /api/lxc/{name}/exec              {command: [...], stdin, timeout}: {exitCode, stdout, stderr}
//	GET    /api/lxc/{name}/console?mode=shell|console   websocket terminal ({type: input|resize})
//	GET    /api/lxc/{name}/snapshots         list
//	POST   /api/lxc/{name}/snapshots         {name, description} (job)
//	POST   /api/lxc/{name}/snapshots/{snap}/restore   (job, container stopped)
//	DELETE /api/lxc/{name}/snapshots/{snap}
//	GET    /api/lxc/{name}/backups?dir=…     backups of the container in a folder
//	POST   /api/lxc/{name}/backup            lxc.ExportOpts (job)

// lxcView is a container as the API shows it: the mapped ids by name too.
type lxcView struct {
	lxc.Container
	MapUsers  []string `json:"mapUsers"`
	MapGroups []string `json:"mapGroups"`
}

func viewOf(c lxc.Container) lxcView {
	v := lxcView{Container: c, MapUsers: []string{}, MapGroups: []string{}}
	for _, id := range c.IDMap.UIDs {
		if u, err := user.LookupId(strconv.Itoa(id)); err == nil {
			v.MapUsers = append(v.MapUsers, u.Username)
		} else {
			v.MapUsers = append(v.MapUsers, strconv.Itoa(id))
		}
	}
	for _, id := range c.IDMap.GIDs {
		if g, err := user.LookupGroupId(strconv.Itoa(id)); err == nil {
			v.MapGroups = append(v.MapGroups, g.Name)
		} else {
			v.MapGroups = append(v.MapGroups, strconv.Itoa(id))
		}
	}
	return v
}

// hostBridges lists the bridges containers and VMs can join (not the private
// ones of libvirt, Docker or lxc-net).
func hostBridges() []string {
	out := []string{}
	entries, _ := os.ReadDir("/sys/class/net")
	for _, e := range entries {
		n := e.Name()
		if _, err := os.Stat("/sys/class/net/" + n + "/bridge"); err != nil {
			continue
		}
		if strings.HasPrefix(n, "virbr") || strings.HasPrefix(n, "docker") || strings.HasPrefix(n, "br-") || strings.HasPrefix(n, "lxcbr") {
			continue
		}
		out = append(out, n)
	}
	return out
}

// lxcIDs turns StrataOS user and group names into the ids kept inside a
// container (a user's primary group comes with it).
func lxcIDs(users, groups []string) (uids, gids []int, err error) {
	for _, n := range users {
		u, err := user.Lookup(n)
		if err != nil {
			return nil, nil, badRequest("unknown user " + n)
		}
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		if uid < 1000 {
			return nil, nil, badRequest("only StrataOS accounts can be mapped into a container")
		}
		uids = append(uids, uid)
		if gid >= 100 && !slices.Contains(gids, gid) {
			gids = append(gids, gid)
		}
	}
	for _, n := range groups {
		g, err := user.LookupGroup(n)
		if err != nil {
			return nil, nil, badRequest("unknown group " + n)
		}
		gid, _ := strconv.Atoi(g.Gid)
		if gid < 100 {
			return nil, nil, badRequest("only StrataOS groups can be mapped into a container")
		}
		if !slices.Contains(gids, gid) {
			gids = append(gids, gid)
		}
	}
	return uids, gids, nil
}

// lxcMounts fills in the path of each mounted shared folder: only shared
// folders of StrataOS can be mounted.
func (s *Server) lxcMounts(mounts []lxc.Mount) ([]lxc.Mount, error) {
	out := make([]lxc.Mount, 0, len(mounts))
	var err error
	s.store.Read(func(st *store.State) {
		for _, m := range mounts {
			sh := st.Share(m.Share)
			if sh == nil {
				err = badRequest("unknown shared folder " + m.Share)
				return
			}
			m.Source = sh.Path
			if strings.TrimSpace(m.Target) == "" {
				m.Target = "/mnt/" + strings.ToLower(strings.ReplaceAll(sh.Name, " ", "-"))
			}
			out = append(out, m)
		}
	})
	return out, err
}

func (s *Server) lxcRoutes() {
	s.admin("GET /api/lxc", func(r *http.Request) (any, error) {
		out := map[string]any{"installed": lxc.Installed(), "bridges": hostBridges(), "natBridge": lxc.NATBridge, "arch": lxc.HostArch(), "containers": []lxcView{}}
		if !lxc.Installed() {
			return out, nil
		}
		list := []lxcView{}
		for _, c := range lxc.List() {
			list = append(list, viewOf(c))
		}
		out["containers"] = list
		return out, nil
	})

	s.admin("GET /api/lxc/templates", func(r *http.Request) (any, error) {
		list, at, err := lxc.Images(r.Context(), r.URL.Query().Get("refresh") == "1")
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return map[string]any{"images": list, "updated": at, "server": lxc.ImageServer}, nil
	})

	s.admin("GET /api/lxc/storage", func(r *http.Request) (any, error) {
		type loc struct {
			Path     string   `json:"path"`
			Label    string   `json:"label"`
			FSType   string   `json:"fsType"`
			Free     int64    `json:"free"`
			Backends []string `json:"backends"` // best first
		}
		out := []loc{}
		var vols []store.Volume
		s.store.Read(func(st *store.State) {
			for _, v := range st.Volumes {
				vols = append(vols, *v)
			}
		})
		for _, v := range vols {
			if !storage.IsMountpoint(v.MountPoint) {
				continue
			}
			u, _ := storage.StatFS(v.MountPoint)
			b := []string{"image", "dir"}
			if v.FSType == "btrfs" {
				b = []string{"btrfs", "image", "dir"}
			}
			out = append(out, loc{Path: v.MountPoint, Label: v.Name, FSType: v.FSType, Free: u.Free, Backends: b})
		}
		if storage.ZFSAvailable() {
			ds, _ := storage.ListZDatasets("", "filesystem")
			for _, d := range ds {
				if strings.HasPrefix(d.Mountpoint, storage.MountRoot+"/") && !strings.Contains(d.Name, "/strata-lxc") {
					out = append(out, loc{Path: d.Mountpoint, Label: d.Name, FSType: "zfs", Free: d.Avail, Backends: []string{"zfs", "image", "dir"}})
				}
			}
		}
		return out, nil
	})

	s.admin("GET /api/lxc/importable", func(r *http.Request) (any, error) {
		return lxc.FindImportable(), nil
	})

	s.admin("POST /api/lxc", func(r *http.Request) (any, error) {
		var req struct {
			lxc.CreateReq
			MapUsers  []string `json:"mapUsers"`
			MapGroups []string `json:"mapGroups"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := lxc.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		if lxc.Exists(req.Name) {
			return nil, badRequest("a container named " + req.Name + " already exists")
		}
		if !lxc.Installed() {
			return nil, badRequest("containers (LXC) are not installed")
		}
		var err error
		if req.UIDs, req.GIDs, err = lxcIDs(req.MapUsers, req.MapGroups); err != nil {
			return nil, err
		}
		if req.Privileged && (len(req.UIDs) > 0 || len(req.GIDs) > 0) {
			return nil, badRequest("a privileged container uses the ids of the NAS as they are: no mapping")
		}
		if req.Mounts, err = s.lxcMounts(req.Mounts); err != nil {
			return nil, err
		}
		if _, err := lxc.CheckLocation(req.Location, req.Backend); err != nil {
			return nil, badRequest(err.Error())
		}
		msg := "Creating container " + req.Name + " (" + req.Image.Distro + " " + req.Image.Release + ")"
		if req.Privileged {
			msg += ", privileged"
		}
		s.audit(r, "vms", msg)
		return s.jobs.Start("lxc", "Creating container "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return lxc.Create(ctx, req.CreateReq, j.Logf)
		}), nil
	})

	s.admin("POST /api/lxc/import", func(r *http.Request) (any, error) {
		var req lxc.ImportReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Name != "" {
			if err := lxc.ValidName(req.Name); err != nil {
				return nil, badRequest(err.Error())
			}
		}
		s.audit(r, "vms", "Importing container from "+req.Source)
		return s.jobs.Start("lxc", "Importing container", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return lxc.Import(ctx, req, j.Logf)
		}), nil
	})

	s.admin("GET /api/lxc/{name}", func(r *http.Request) (any, error) {
		c, err := lxc.Get(r.PathValue("name"))
		if err != nil {
			return nil, notFound(err.Error())
		}
		return viewOf(*c), nil
	})

	s.admin("PUT /api/lxc/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		var req struct {
			lxc.Settings
			MapUsers  []string `json:"mapUsers"`
			MapGroups []string `json:"mapGroups"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var err error
		if req.UIDs, req.GIDs, err = lxcIDs(req.MapUsers, req.MapGroups); err != nil {
			return nil, err
		}
		if req.Mounts, err = s.lxcMounts(req.Mounts); err != nil {
			return nil, err
		}
		restart, err := lxc.Update(r.Context(), name, req.Settings)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", "Container "+name+" settings changed")
		return map[string]bool{"restart": restart}, nil
	})

	s.admin("DELETE /api/lxc/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		if err := lxc.Delete(r.Context(), name); err != nil {
			return nil, badRequest(err.Error())
		}
		s.dropContainerFromBackups(name)
		s.audit(r, "vms", "Deleted container "+name)
		return nil, nil
	})

	s.admin("POST /api/lxc/{name}/{action}", func(r *http.Request) (any, error) {
		name, action := r.PathValue("name"), r.PathValue("action")
		var err error
		switch action {
		case "start":
			err = lxc.Start(r.Context(), name)
		case "stop":
			err = lxc.Stop(name)
		case "restart":
			err = lxc.Restart(r.Context(), name)
		case "kill":
			err = lxc.Kill(name)
		default:
			return nil, notFound("unknown action")
		}
		if err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", fmt.Sprintf("Container %s: %s", name, action))
		return nil, nil
	})

	s.admin("POST /api/lxc/{name}/clone", func(r *http.Request) (any, error) {
		src := r.PathValue("name")
		var req struct {
			Name string `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := lxc.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		if _, err := lxc.Load(src); err != nil {
			return nil, notFound(err.Error())
		}
		s.audit(r, "vms", "Cloning container "+src+" into "+req.Name)
		return s.jobs.Start("lxc", "Cloning container "+src+" into "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return lxc.Clone(ctx, src, req.Name, j.Logf)
		}), nil
	})

	s.admin("POST /api/lxc/{name}/rename", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		var req struct {
			Name string `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := lxc.Rename(r.Context(), name, req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		s.renameContainerInBackups(name, req.Name)
		s.audit(r, "vms", "Container "+name+" renamed to "+req.Name)
		return nil, nil
	})

	s.admin("GET /api/lxc/{name}/stats", func(r *http.Request) (any, error) {
		st, err := lxc.GetStats(r.PathValue("name"), r.URL.Query().Get("disk") == "1")
		if err != nil {
			return nil, notFound(err.Error())
		}
		return st, nil
	})

	s.admin("POST /api/lxc/{name}/exec", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		var req struct {
			Command []string `json:"command"`
			Stdin   string   `json:"stdin"`
			Timeout int      `json:"timeout"` // seconds, default 60, at most 3600
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Timeout <= 0 {
			req.Timeout = 60
		}
		s.audit(r, "vms", "Command run in container "+name+": "+strings.Join(req.Command, " "))
		out, errOut, code, err := lxc.Exec(r.Context(), name, req.Command, req.Stdin, time.Duration(min(req.Timeout, 3600))*time.Second)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		return map[string]any{"exitCode": code, "stdout": out, "stderr": errOut}, nil
	})

	s.adminRaw("GET /api/lxc/{name}/console", s.lxcConsole)

	s.admin("GET /api/lxc/{name}/snapshots", func(r *http.Request) (any, error) {
		list, err := lxc.Snapshots(r.PathValue("name"))
		if err != nil {
			return nil, notFound(err.Error())
		}
		return list, nil
	})
	s.admin("POST /api/lxc/{name}/snapshots", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		var req struct{ Name, Description string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if _, err := lxc.Load(name); err != nil {
			return nil, notFound(err.Error())
		}
		s.audit(r, "vms", "Snapshot "+req.Name+" of container "+name)
		return s.jobs.Start("lxc", "Snapshot of container "+name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return lxc.CreateSnapshot(ctx, name, req.Name, req.Description)
		}), nil
	})
	s.admin("POST /api/lxc/{name}/snapshots/{snap}/restore", func(r *http.Request) (any, error) {
		name, snap := r.PathValue("name"), r.PathValue("snap")
		if _, err := lxc.Load(name); err != nil {
			return nil, notFound(err.Error())
		}
		s.audit(r, "vms", "Container "+name+" restored to snapshot "+snap)
		return s.jobs.Start("lxc", "Restoring container "+name+" to "+snap, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return lxc.RestoreSnapshot(ctx, name, snap)
		}), nil
	})
	s.admin("DELETE /api/lxc/{name}/snapshots/{snap}", func(r *http.Request) (any, error) {
		name, snap := r.PathValue("name"), r.PathValue("snap")
		if err := lxc.DeleteSnapshot(r.Context(), name, snap); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "vms", "Snapshot "+snap+" of container "+name+" deleted")
		return nil, nil
	})

	s.admin("GET /api/lxc/{name}/backups", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		if err := lxc.ValidName(name); err != nil {
			return nil, badRequest(err.Error())
		}
		dir := r.URL.Query().Get("dir")
		out := []lxc.Archive{}
		if dir == "" || !strings.HasPrefix(dir, storage.MountRoot+"/") || strings.Contains(dir, "..") {
			return out, nil
		}
		for _, a := range lxc.Archives(dir) {
			if a.Container == name {
				out = append(out, a)
			}
		}
		return out, nil
	})
	s.admin("POST /api/lxc/{name}/backup", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		var req lxc.ExportOpts
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.File, req.AnyDir = "", false
		if _, err := lxc.Load(name); err != nil {
			return nil, notFound(err.Error())
		}
		if !lxc.ValidMode(req.Mode) || req.Keep < 0 || req.Keep > 1000 {
			return nil, badRequest("invalid backup settings")
		}
		s.audit(r, "vms", "Backup of container "+name+" to "+req.Dir)
		return s.jobs.Start("lxc", "Backup of container "+name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			_, _, err := lxc.Export(ctx, name, req, j.Logf)
			return err
		}), nil
	})
}

// dropContainerFromBackups removes a deleted container from the backup tasks.
func (s *Server) dropContainerFromBackups(name string) {
	_ = s.store.Update(func(st *store.State) error {
		for _, b := range st.Backups {
			b.Containers = slices.DeleteFunc(b.Containers, func(n string) bool { return n == name })
		}
		return nil
	})
}

func (s *Server) renameContainerInBackups(old, name string) {
	_ = s.store.Update(func(st *store.State) error {
		for _, b := range st.Backups {
			for i, n := range b.Containers {
				if n == old {
					b.Containers[i] = name
				}
			}
		}
		return nil
	})
}

// lxcConsole is an interactive terminal over a websocket (the protocol of the
// host terminal: binary output, JSON input and resize messages): mode=shell
// runs root's shell (lxc-attach), mode=console attaches to the container's
// console, where its login prompt is (Ctrl+a q leaves it).
func (s *Server) lxcConsole(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := lxc.ValidName(name); err != nil || !lxc.Exists(name) {
		writeErr(w, notFound("container not found"))
		return
	}
	if lxc.State(name) != "running" {
		writeErr(w, badRequest(name+" is not running"))
		return
	}
	mode := r.URL.Query().Get("mode")
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cmd := lxc.ShellCmd(ctx, name)
	if mode == "console" {
		cmd = lxc.ConsoleCmd(ctx, name)
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 32})
	if err != nil {
		_ = c.Write(ctx, websocket.MessageText, []byte("failed to start: "+err.Error()))
		return
	}
	defer func() {
		f.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	if mode == "console" {
		s.audit(r, "vms", "Opened the console of container "+name)
	} else {
		s.audit(r, "vms", "Opened a shell in container "+name)
	}
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				if c.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					cancel()
					return
				}
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m termMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "input":
			_, _ = f.Write([]byte(m.Data))
		case "resize":
			if m.Cols > 0 && m.Rows > 0 && m.Cols < 1000 && m.Rows < 1000 {
				_ = pty.Setsize(f, &pty.Winsize{Cols: uint16(m.Cols), Rows: uint16(m.Rows)})
			}
		}
	}
}

// lxcAutostartLoop starts the "start with the NAS" containers once per boot.
func (s *Server) lxcAutostartLoop() {
	for !lxc.Installed() {
		time.Sleep(time.Minute)
	}
	if err := lxc.EnsureUnit(); err != nil {
		slog.Warn("container unit", "err", err)
	}
	if lxc.StartedThisBoot() {
		return
	}
	lxc.MarkStarted()
	lxc.StartAll(context.Background(), func(name string, err error) {
		if err != nil {
			msg := "Container " + name + " did not start with the NAS: " + err.Error()
			s.store.Log("error", "vms", "", msg)
			s.notify("error", "Container not started", msg)
		} else {
			s.store.Log("info", "vms", "", "Container "+name+" started with the NAS")
		}
	})
}
