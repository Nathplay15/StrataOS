package api

// Ceph as storage for this NAS: CephFS mounted as a volume, RBD images
// formatted as volumes, libvirt RBD pools for VM disks, from the NAS's own
// cluster ("local") or from external clusters (monitors + cephx user + key,
// the key kept in /etc/ceph/strata only). Also the background loop: mounts
// after boot and health alerts.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"strataos/internal/ceph"
	"strataos/internal/jobs"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/vms"
)

// cephSource is a Ceph cluster this NAS uses as storage.
type cephSource struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	FSID   string    `json:"fsid"`
	Mons   []string  `json:"mons"`
	User   string    `json:"user"`
	Local  bool      `json:"local"`
	Added  time.Time `json:"added,omitzero"`
	conn   ceph.Conn
	secret string // secret file for mount.ceph
}

func (s *Server) cephSources() []cephSource {
	var out []cephSource
	l := s.cephLocal()
	if l != nil {
		src := cephSource{ID: ceph.LocalCluster, Name: s.clusterState().Name, FSID: l.FSID, User: "admin", Local: true, conn: ceph.Local}
		for _, m := range l.Mons {
			src.Mons = append(src.Mons, m.Addr)
		}
		out = append(out, src)
	}
	s.store.Read(func(st *store.State) {
		for _, e := range st.Ceph.External {
			c := ceph.External(e.ID, e.User)
			out = append(out, cephSource{ID: e.ID, Name: e.Name, FSID: e.FSID, Mons: slices.Clone(e.Mons), User: e.User, Added: e.Added, conn: c, secret: c.SecretFile()})
		}
	})
	return out
}

func (s *Server) cephSource(id string) (cephSource, error) {
	for _, src := range s.cephSources() {
		if src.ID == id {
			if src.Local {
				p, err := ceph.LocalSecretFile()
				if err != nil {
					return src, err
				}
				src.secret = p
			}
			return src, nil
		}
	}
	return cephSource{}, notFound("Ceph cluster not found")
}

func (s *Server) cephVolumeOf(volumeID string) (store.CephVolume, bool) {
	var cv store.CephVolume
	var ok bool
	s.store.Read(func(st *store.State) {
		for _, v := range st.Ceph.Volumes {
			if v.VolumeID == volumeID {
				cv, ok = v, true
			}
		}
	})
	return cv, ok
}

// cephMountVolume maps (RBD) and mounts a Ceph volume.
func (s *Server) cephMountVolume(ctx context.Context, v store.Volume, cv store.CephVolume) error {
	if storage.IsMountpoint(v.MountPoint) {
		return nil
	}
	src, err := s.cephSource(cv.Cluster)
	if err != nil {
		return err
	}
	if cv.Kind == "rbd" {
		if _, err := src.conn.Map(ctx, src.FSID, cv.Pool, cv.Image); err != nil {
			return err
		}
	}
	return storage.MountPoint(v.MountPoint)
}

// cephDetachVolume runs after a Ceph volume was unmounted and removed:
// unmaps its image, deletes it when asked and StrataOS created it.
func (s *Server) cephDetachVolume(ctx context.Context, v store.Volume, wipe bool) error {
	cv, ok := s.cephVolumeOf(v.ID)
	if !ok {
		return nil
	}
	var err error
	if cv.Kind == "rbd" {
		src, serr := s.cephSource(cv.Cluster)
		fsid := ""
		if serr == nil {
			fsid = src.FSID
		}
		if err = ceph.Unmap(fsid, cv.Pool, cv.Image); err == nil && wipe && cv.Created && serr == nil {
			err = src.conn.RemoveImage(ctx, cv.Pool, cv.Image)
		}
	}
	_ = s.store.Update(func(st *store.State) error {
		st.Ceph.Volumes = slices.DeleteFunc(st.Ceph.Volumes, func(x store.CephVolume) bool { return x.VolumeID == v.ID })
		return nil
	})
	return err
}

type cephVolumeReq struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Kind    string `json:"kind"` // cephfs | rbd
	FS      string `json:"fs"`
	Path    string `json:"path"`
	Pool    string `json:"pool"`
	Image   string `json:"image"`
	Create  bool   `json:"create"` // new RBD image of SizeGB
	SizeGB  int    `json:"sizeGb"`
	FSType  string `json:"fsType"` // ext4 | xfs | btrfs (new image)
	Confirm bool   `json:"confirm"`
}

func (s *Server) cephCreateVolume(ctx context.Context, req cephVolumeReq, j *jobs.Job) error {
	src, err := s.cephSource(req.Source)
	if err != nil {
		return err
	}
	mp := filepath.Join(storage.MountRoot, req.Name)
	vol := store.Volume{ID: shortID(), Name: req.Name, MountPoint: mp, CreatedAt: time.Now()}
	cv := store.CephVolume{VolumeID: vol.ID, Cluster: src.ID, Kind: req.Kind}
	switch req.Kind {
	case "cephfs":
		sub, err := ceph.CleanSubPath(req.Path)
		if err != nil {
			return err
		}
		if src.FSID == "" {
			return fmt.Errorf("the fsid of this cluster is unknown: connect it again")
		}
		j.Logf("CephFS %s%s from %s", req.FS, sub, src.Name)
		if err := ceph.EnsureFSPath(ctx, src.User, src.FSID, req.FS, sub, src.secret, src.Mons); err != nil {
			return err
		}
		source := ceph.CephFSSource(src.User, src.FSID, req.FS, sub)
		if err := storage.PersistNetMount(source, mp, "ceph", ceph.CephFSOptions(src.secret, src.Mons)+",acl"); err != nil {
			return err
		}
		if err := storage.MountPoint(mp); err != nil {
			_ = storage.UnmountVolume(mp)
			return err
		}
		vol.FSType, vol.Devices = "cephfs", []string{"cephfs:" + req.FS + sub}
		cv.FS, cv.Path = req.FS, sub
	case "rbd":
		cv.Pool, cv.Image = req.Pool, req.Image
		if req.Create {
			j.Logf("New RBD image %s/%s of %d GB", req.Pool, req.Image, req.SizeGB)
			if err := src.conn.CreateImage(ctx, req.Pool, req.Image, req.SizeGB); err != nil {
				return err
			}
			cv.Created = true
		}
		dev, err := src.conn.Map(ctx, src.FSID, req.Pool, req.Image)
		if err != nil {
			if cv.Created {
				_ = src.conn.RemoveImage(ctx, req.Pool, req.Image)
			}
			return err
		}
		j.Logf("Mapped as %s", dev)
		fail := func(err error) error {
			_ = ceph.Unmap(src.FSID, req.Pool, req.Image)
			if cv.Created {
				_ = src.conn.RemoveImage(ctx, req.Pool, req.Image)
			}
			return err
		}
		if fs, _, _ := storage.Probe(dev); fs == "" {
			uuid, _, err := storage.CreateFilesystem(ctx, storage.CreateFSReq{Name: req.Name, FSType: req.FSType, Devices: []string{dev}, MountOpts: "_netdev,noauto"}, j.Logf)
			if err != nil {
				return fail(err)
			}
			vol.FSType, vol.UUID = req.FSType, uuid
		} else {
			fstype, uuid, _, err := storage.MountExistingOpts(req.Name, dev, "_netdev,noauto")
			if err != nil {
				return fail(err)
			}
			vol.FSType, vol.UUID = fstype, uuid
			j.Logf("Existing %s filesystem mounted", fstype)
		}
		vol.Devices = []string{dev}
	}
	return s.store.Update(func(st *store.State) error {
		st.Volumes = append(st.Volumes, &vol)
		st.Ceph.Volumes = append(st.Ceph.Volumes, cv)
		return nil
	})
}

func (s *Server) cephStorageRoutes() {
	s.admin("GET /api/ceph/storage", func(r *http.Request) (any, error) {
		type volView struct {
			store.CephVolume
			Name    string        `json:"name"`
			Mounted bool          `json:"mounted"`
			Usage   storage.Usage `json:"usage"`
		}
		type poolView struct {
			store.CephVMPool
			Active bool `json:"active"`
		}
		srcs := s.cephSources()
		if srcs == nil {
			srcs = []cephSource{}
		}
		var cvs []store.CephVolume
		var pools []store.CephVMPool
		vols := map[string]store.Volume{}
		s.store.Read(func(st *store.State) {
			cvs = slices.Clone(st.Ceph.Volumes)
			pools = slices.Clone(st.Ceph.VMPools)
			for _, v := range st.Volumes {
				vols[v.ID] = *v
			}
		})
		vv := []volView{}
		for _, cv := range cvs {
			v := vols[cv.VolumeID]
			x := volView{CephVolume: cv, Name: v.Name, Mounted: v.MountPoint != "" && storage.IsMountpoint(v.MountPoint)}
			if x.Mounted {
				x.Usage, _ = storage.StatFS(v.MountPoint)
			}
			vv = append(vv, x)
		}
		pv := []poolView{}
		for _, p := range pools {
			pv = append(pv, poolView{CephVMPool: p, Active: vms.Installed() && vms.PoolActive(p.Name)})
		}
		return map[string]any{"sources": srcs, "volumes": vv, "vmPools": pv, "vmsInstalled": vms.Installed(), "clientInstalled": ceph.ClientInstalled()}, nil
	})

	// Tests an external cluster without saving it.
	s.admin("POST /api/ceph/external/test", func(r *http.Request) (any, error) {
		var req struct {
			Mons string `json:"mons"`
			User string `json:"user"`
			Key  string `json:"key"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		mons, user, err := cephExternalCheck(req.Mons, req.User, req.Key)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		id := "test-" + shortID()
		defer ceph.RemoveExternal(id)
		conn, err := ceph.SaveExternal(id, user, req.Key, "", mons)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		return cephProbe(ctx, conn)
	})

	s.admin("POST /api/ceph/external", func(r *http.Request) (any, error) {
		var req struct {
			Name string `json:"name"`
			Mons string `json:"mons"`
			User string `json:"user"`
			Key  string `json:"key"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" || len(req.Name) > 64 || strings.ContainsAny(req.Name, "\n\r\t") {
			return nil, badRequest("invalid name")
		}
		mons, user, err := cephExternalCheck(req.Mons, req.User, req.Key)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		id := "ext-" + shortID()
		conn, err := ceph.SaveExternal(id, user, req.Key, "", mons)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		fsid, err := conn.FSID(ctx)
		if err != nil {
			ceph.RemoveExternal(id)
			return nil, badRequest("cannot reach the cluster: " + err.Error())
		}
		if l := s.cephLocal(); l != nil && l.FSID == fsid && user == "admin" {
			ceph.RemoveExternal(id)
			return nil, badRequest("this is the Ceph cluster of this NAS: use it directly")
		}
		if _, err := ceph.SaveExternal(id, user, req.Key, fsid, mons); err != nil {
			return nil, err
		}
		e := store.CephExternal{ID: id, Name: req.Name, Mons: mons, User: user, FSID: fsid, Added: time.Now()}
		if err := s.store.Update(func(st *store.State) error {
			if len(st.Ceph.External) >= 20 {
				return badRequest("too many external Ceph clusters")
			}
			st.Ceph.External = append(st.Ceph.External, e)
			return nil
		}); err != nil {
			ceph.RemoveExternal(id)
			return nil, err
		}
		s.audit(r, "ceph", "External Ceph cluster added: "+req.Name)
		return e, nil
	})

	s.admin("DELETE /api/ceph/external/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		var used bool
		var found bool
		s.store.Read(func(st *store.State) {
			for _, e := range st.Ceph.External {
				found = found || e.ID == id
			}
			for _, v := range st.Ceph.Volumes {
				used = used || v.Cluster == id
			}
			for _, p := range st.Ceph.VMPools {
				used = used || p.Cluster == id
			}
		})
		if !found {
			return nil, notFound("Ceph cluster not found")
		}
		if used {
			return nil, badRequest("volumes or VM storage still use this cluster: remove them first")
		}
		_ = s.store.Update(func(st *store.State) error {
			st.Ceph.External = slices.DeleteFunc(st.Ceph.External, func(e store.CephExternal) bool { return e.ID == id })
			return nil
		})
		ceph.RemoveExternal(id)
		s.audit(r, "ceph", "External Ceph cluster removed")
		return nil, nil
	})

	// What a cluster offers: pools, filesystems (and images of a pool).
	s.admin("GET /api/ceph/sources/{id}", func(r *http.Request) (any, error) {
		src, err := s.cephSource(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		if pool := r.URL.Query().Get("images"); pool != "" {
			if err := ceph.ValidPool(pool); err != nil {
				return nil, badRequest(err.Error())
			}
			return src.conn.Images(ctx, pool)
		}
		return cephProbe(ctx, src.conn)
	})

	s.admin("POST /api/ceph/volumes", func(r *http.Request) (any, error) {
		var req cephVolumeReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := storage.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		if s.volumeNameTaken(req.Name) {
			return nil, badRequest("a volume with this name already exists")
		}
		if !ceph.ClientInstalled() {
			return nil, badRequest("install the Ceph component first (Settings → Components)")
		}
		if _, err := s.cephSource(req.Source); err != nil {
			return nil, badRequest(err.Error())
		}
		switch req.Kind {
		case "cephfs":
			if err := ceph.ValidPool(req.FS); err != nil {
				return nil, badRequest(err.Error())
			}
			if _, err := ceph.CleanSubPath(req.Path); err != nil {
				return nil, badRequest(err.Error())
			}
		case "rbd":
			if err := ceph.ValidPool(req.Pool); err != nil {
				return nil, badRequest(err.Error())
			}
			if err := ceph.ValidImage(req.Image); err != nil {
				return nil, badRequest(err.Error())
			}
			if req.Create {
				if req.SizeGB < 1 || req.SizeGB > 1<<20 {
					return nil, badRequest("invalid size")
				}
				if req.FSType != "ext4" && req.FSType != "xfs" && req.FSType != "btrfs" {
					return nil, badRequest("choose ext4, XFS or Btrfs")
				}
			}
			if !req.Confirm {
				return nil, badRequest("an RBD volume can be used by one NAS only: confirm that no other NAS uses this image")
			}
		default:
			return nil, badRequest("choose CephFS or RBD")
		}
		s.audit(r, "ceph", fmt.Sprintf("Creating the Ceph volume %s (%s)", req.Name, req.Kind))
		return s.jobs.Start("volume", "Create volume "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return s.cephCreateVolume(ctx, req, j)
		}), nil
	})

	s.admin("GET /api/ceph/vm-pools", func(r *http.Request) (any, error) {
		type row struct {
			Name   string `json:"name"`
			Label  string `json:"label"`
			Active bool   `json:"active"`
		}
		out := []row{}
		var pools []store.CephVMPool
		s.store.Read(func(st *store.State) { pools = slices.Clone(st.Ceph.VMPools) })
		names := map[string]string{}
		for _, src := range s.cephSources() {
			names[src.ID] = src.Name
		}
		for _, p := range pools {
			out = append(out, row{Name: p.Name, Label: fmt.Sprintf("Ceph %s: %s", firstNonEmpty(names[p.Cluster], p.Cluster), p.Pool), Active: vms.Installed() && vms.PoolActive(p.Name)})
		}
		return out, nil
	})

	s.admin("POST /api/ceph/vm-pools", func(r *http.Request) (any, error) {
		var req struct {
			Source string `json:"source"`
			Pool   string `json:"pool"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if !vms.Installed() {
			return nil, badRequest("install the Virtual machines component first")
		}
		if err := ceph.ValidPool(req.Pool); err != nil {
			return nil, badRequest(err.Error())
		}
		src, err := s.cephSource(req.Source)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		name := "ceph-" + strings.TrimPrefix(src.ID, "ext-") + "-" + req.Pool
		if err := vms.ValidPoolName(name); err != nil {
			return nil, badRequest(err.Error())
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		user, key := src.User, ""
		if src.Local {
			// A key limited to RBD for libvirt, rather than the admin key.
			user = "strata-vms"
			out, err := ceph.Local.Ceph(ctx, "auth", "get-or-create", "client."+user, "mon", "profile rbd", "osd", "profile rbd", "mgr", "profile rbd")
			if err != nil {
				return nil, err
			}
			key = ceph.KeyOf(out, "client."+user)
		} else if data, err := os.ReadFile(src.secret); err == nil {
			key = strings.TrimSpace(string(data))
		}
		if key == "" {
			return nil, errors.New("no key for this cluster")
		}
		keyFile := filepath.Join(ceph.StrataDir, "libvirt-"+shortID()+".key")
		if err := os.MkdirAll(ceph.StrataDir, 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(keyFile, []byte(key), 0o600); err != nil {
			return nil, err
		}
		defer os.Remove(keyFile)
		uuid := ceph.NewFSID()
		poolXML := ceph.LibvirtPoolXML(name, req.Pool, user, uuid, src.Mons)
		if err := vms.DefineRBDPool(name, poolXML, ceph.LibvirtSecretXML(uuid, "client."+user+" "+name), uuid, keyFile); err != nil {
			return nil, badRequest(err.Error())
		}
		p := store.CephVMPool{Name: name, Cluster: src.ID, Pool: req.Pool, User: user, Secret: uuid}
		_ = s.store.Update(func(st *store.State) error {
			st.Ceph.VMPools = append(st.Ceph.VMPools, p)
			return nil
		})
		s.audit(r, "ceph", "VM storage on Ceph added: "+name)
		return p, nil
	})

	s.admin("DELETE /api/ceph/vm-pools/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		var p *store.CephVMPool
		s.store.Read(func(st *store.State) {
			for _, x := range st.Ceph.VMPools {
				if x.Name == name {
					c := x
					p = &c
				}
			}
		})
		if p == nil {
			return nil, notFound("VM storage not found")
		}
		if vms.Installed() {
			if err := vms.RemoveRBDPool(p.Name, p.Secret); err != nil {
				return nil, badRequest(err.Error())
			}
		}
		_ = s.store.Update(func(st *store.State) error {
			st.Ceph.VMPools = slices.DeleteFunc(st.Ceph.VMPools, func(x store.CephVMPool) bool { return x.Name == name })
			return nil
		})
		s.audit(r, "ceph", "VM storage on Ceph removed: "+name)
		return nil, nil
	})
}

// cephExternalCheck validates the connection fields of an external cluster.
func cephExternalCheck(monsText, user, key string) ([]string, string, error) {
	mons, err := ceph.ParseMons(monsText)
	if err != nil {
		return nil, "", err
	}
	user = strings.TrimPrefix(strings.TrimSpace(user), "client.")
	if err := ceph.ValidUser(user); err != nil {
		return nil, "", err
	}
	if err := ceph.ValidKey(strings.TrimSpace(key)); err != nil {
		return nil, "", err
	}
	return mons, user, nil
}

// cephProbe tells what a client sees of a cluster. A restricted client may
// not read the health or pool list: what it can read is returned.
func cephProbe(ctx context.Context, c ceph.Conn) (any, error) {
	fsid, err := c.FSID(ctx)
	if err != nil {
		return nil, badRequest("cannot reach the cluster: " + err.Error())
	}
	out := map[string]any{"fsid": fsid, "pools": []ceph.Pool{}, "filesystems": []ceph.FS{}}
	if st, err := c.Status(ctx); err == nil {
		out["health"] = st.Health.Status
	}
	if pools, fss, err := c.Pools(ctx); err == nil {
		out["pools"], out["filesystems"] = pools, fss
	} else {
		out["poolsError"] = err.Error()
	}
	return out, nil
}

// cephLoop mounts the Ceph volumes once their cluster answers (after a boot,
// a network or cluster outage) and turns health changes into notifications.
func (s *Server) cephLoop() {
	time.Sleep(20 * time.Second)
	fails := 0
	for tick := 0; ; tick++ {
		var cvs []store.CephVolume
		vols := map[string]store.Volume{}
		var local bool
		s.store.Read(func(st *store.State) {
			cvs = slices.Clone(st.Ceph.Volumes)
			for _, v := range st.Volumes {
				vols[v.ID] = *v
			}
			local = st.Ceph.Local != nil
		})
		for _, cv := range cvs {
			v, ok := vols[cv.VolumeID]
			if !ok || storage.IsMountpoint(v.MountPoint) {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			err := s.cephMountVolume(ctx, v, cv)
			cancel()
			if err != nil {
				slog.Debug("ceph volume mount", "volume", v.Name, "err", err)
				continue
			}
			s.store.Log("info", "ceph", "", "Ceph volume "+v.Name+" mounted")
		}
		if local && tick%2 == 0 {
			fails = s.cephHealthCheck(fails)
		}
		time.Sleep(30 * time.Second)
	}
}

// cephHealthCheck notifies a health change once per cluster: from the NAS
// whose monitor leads the quorum (any NAS when the monitors are unreachable).
func (s *Server) cephHealthCheck(fails int) int {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var last string
	s.store.Read(func(st *store.State) { last = st.Ceph.LastHealth })
	setLast := func(h string) {
		_ = s.store.Update(func(st *store.State) error { st.Ceph.LastHealth = h; return nil })
	}
	st, err := ceph.Local.Status(ctx)
	if err != nil {
		fails++
		if fails == 4 && last != "UNREACHABLE" {
			msg := "The Ceph cluster does not answer: its volumes are unavailable until enough NAS of the cluster are back"
			s.store.Log("error", "ceph", "", msg)
			s.notify("error", "Ceph cluster unreachable", msg)
			setLast("UNREACHABLE")
		}
		return fails
	}
	fails = 0
	var q struct {
		Leader string `json:"quorum_leader_name"`
	}
	if ceph.Local.JSON(ctx, &q, "quorum_status") != nil || q.Leader != sysHostname() {
		if st.Health.Status != last {
			setLast(st.Health.Status) // keep track without notifying twice
		}
		return 0
	}
	if st.Health.Status == last {
		return 0
	}
	setLast(st.Health.Status)
	var msgs []string
	for _, c := range st.Health.Checks {
		if !c.Muted {
			msgs = append(msgs, c.Message)
		}
	}
	detail := strings.Join(msgs, "; ")
	switch st.Health.Status {
	case "HEALTH_OK":
		if last != "" {
			msg := "The Ceph cluster is healthy again"
			s.store.Log("info", "ceph", "", msg)
			s.notify("info", "Ceph cluster healthy", msg)
		}
	case "HEALTH_WARN":
		msg := fmt.Sprintf("Ceph cluster warning: %s", detail)
		s.store.Log("warning", "ceph", "", msg)
		s.notify("warning", "Ceph cluster warning", msg)
	default:
		msg := fmt.Sprintf("Ceph cluster error: %s", detail)
		s.store.Log("error", "ceph", "", msg)
		s.notify("error", "Ceph cluster error", msg)
	}
	return 0
}
