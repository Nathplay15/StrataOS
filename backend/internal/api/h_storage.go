package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/storage"
	"strataos/internal/store"
)

func shortID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type volumeView struct {
	store.Volume
	Mounted bool          `json:"mounted"`
	Usage   storage.Usage `json:"usage"`
	Shares  []string      `json:"shares"`
	Health  string        `json:"health"`
}

func (s *Server) storageRoutes() {
	// ---- Disks ----
	s.admin("GET /api/storage/disks", func(r *http.Request) (any, error) {
		disks, err := storage.ListDisks()
		if err != nil {
			return nil, err
		}
		type diskView struct {
			storage.BlockDevice
			Temperature  int   `json:"temperature"`
			Healthy      *bool `json:"healthy"`
			PowerOnHours int   `json:"powerOnHours"`
			Standby      bool  `json:"standby,omitempty"`
		}
		out := make([]diskView, 0, len(disks))
		for _, d := range disks {
			v := diskView{BlockDevice: d}
			if info, err := storage.SmartIfAwake(d.Path); err == nil {
				v.Temperature, v.Healthy, v.PowerOnHours, v.Standby = info.Temperature, info.Passed, info.PowerOnHours, info.Standby
			}
			out = append(out, v)
		}
		return out, nil
	})
	s.admin("GET /api/storage/block", func(r *http.Request) (any, error) { return storage.ListBlock() })
	s.admin("GET /api/storage/disks/smart", func(r *http.Request) (any, error) {
		return storage.Smart(r.URL.Query().Get("device"))
	})
	s.admin("POST /api/storage/disks/smart-test", func(r *http.Request) (any, error) {
		var req struct{ Device, Type string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := storage.SmartTest(req.Device, req.Type); err != nil {
			return nil, err
		}
		s.audit(r, "storage", fmt.Sprintf("SMART %s test started on %s", req.Type, req.Device))
		return nil, nil
	})
	s.admin("POST /api/storage/disks/wipe", func(r *http.Request) (any, error) {
		var req struct{ Device string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		storage.Lock.Lock()
		err := storage.Wipe(req.Device)
		storage.Lock.Unlock()
		if err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "storage", "Wiped disk "+req.Device)
		return nil, nil
	})

	// ---- mdadm RAID ----
	s.admin("GET /api/storage/raid", func(r *http.Request) (any, error) { return storage.ListRaid() })
	s.admin("POST /api/storage/raid", func(r *http.Request) (any, error) {
		var req storage.CreateRaidReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := storage.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		if err := storage.ValidateFreeDisks(append(append([]string{}, req.Devices...), req.Spares...), false); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "storage", fmt.Sprintf("Creating RAID %s (level %s) on %s", req.Name, req.Level, strings.Join(req.Devices, ", ")))
		return s.jobs.Start("raid", "Create RAID "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			dev, err := storage.CreateRaid(ctx, req, j.Logf)
			if err == nil {
				j.Logf("Array %s created. Initial sync runs in the background; the array is usable now.", dev)
			}
			return err
		}), nil
	})
	s.admin("POST /api/storage/raid/{name}/{action}", func(r *http.Request) (any, error) {
		var req struct {
			Device string `json:"device"`
			Count  int    `json:"count"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name, action := r.PathValue("name"), r.PathValue("action")
		if err := storage.RaidAction(name, action, req.Device, req.Count); err != nil {
			return nil, err
		}
		s.audit(r, "storage", fmt.Sprintf("RAID %s: %s %s", name, action, req.Device))
		return nil, nil
	})
	s.admin("DELETE /api/storage/raid/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		if err := storage.DeleteRaid(name); err != nil {
			return nil, err
		}
		s.audit(r, "storage", "Deleted RAID "+name)
		return nil, nil
	})

	// ---- LVM ----
	s.admin("GET /api/storage/lvm", func(r *http.Request) (any, error) { return storage.ListLVM() })
	s.admin("POST /api/storage/lvm", func(r *http.Request) (any, error) {
		var req struct {
			Name    string   `json:"name"`
			Devices []string `json:"devices"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := storage.CreateVG(req.Name, req.Devices); err != nil {
			return nil, err
		}
		s.audit(r, "storage", "Created volume group "+req.Name)
		return nil, nil
	})
	s.admin("POST /api/storage/lvm/{vg}/extend", func(r *http.Request) (any, error) {
		var req struct {
			Devices []string `json:"devices"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return nil, storage.ExtendVG(r.PathValue("vg"), req.Devices)
	})
	s.admin("DELETE /api/storage/lvm/{vg}", func(r *http.Request) (any, error) {
		if err := storage.RemoveVG(r.PathValue("vg")); err != nil {
			return nil, err
		}
		s.audit(r, "storage", "Deleted volume group "+r.PathValue("vg"))
		return nil, nil
	})
	// SSD cache of a logical volume (attached and removed while mounted).
	s.admin("POST /api/storage/lvm/{vg}/lv/{lv}/cache", func(r *http.Request) (any, error) {
		var req struct {
			Device string `json:"device"`
			Mode   string `json:"mode"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		vg, lv := r.PathValue("vg"), r.PathValue("lv")
		if req.Mode == "" {
			req.Mode = "writethrough"
		}
		s.audit(r, "storage", fmt.Sprintf("SSD cache %s (%s) added to %s/%s", req.Device, req.Mode, vg, lv))
		return s.jobs.Start("storage", "Cache SSD pour "+vg+"/"+lv, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return storage.AttachCache(vg, lv, req.Device, req.Mode)
		}), nil
	})
	s.admin("DELETE /api/storage/lvm/{vg}/lv/{lv}/cache", func(r *http.Request) (any, error) {
		vg, lv := r.PathValue("vg"), r.PathValue("lv")
		s.audit(r, "storage", "SSD cache removed from "+vg+"/"+lv)
		return s.jobs.Start("storage", "Retrait du cache SSD de "+vg+"/"+lv, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			j.Logf("Copie des données en attente vers les disques, puis retrait du cache…")
			return storage.DetachCache(vg, lv)
		}), nil
	})
	s.admin("POST /api/storage/lvm/{vg}/lv", func(r *http.Request) (any, error) {
		var req storage.CreateLVReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := storage.CreateLV(r.PathValue("vg"), req); err != nil {
			return nil, err
		}
		s.audit(r, "storage", fmt.Sprintf("Created logical volume %s/%s", r.PathValue("vg"), req.Name))
		return nil, nil
	})
	s.admin("POST /api/storage/lvm/{vg}/lv/{lv}/resize", func(r *http.Request) (any, error) {
		var req struct {
			Size int64 `json:"size"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return nil, storage.ResizeLV(r.PathValue("vg"), r.PathValue("lv"), req.Size)
	})
	s.admin("POST /api/storage/lvm/{vg}/lv/{lv}/snapshot", func(r *http.Request) (any, error) {
		var req struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return nil, storage.SnapshotLV(r.PathValue("vg"), r.PathValue("lv"), req.Name, req.Size)
	})
	s.admin("DELETE /api/storage/lvm/{vg}/lv/{lv}", func(r *http.Request) (any, error) {
		if err := storage.RemoveLV(r.PathValue("vg"), r.PathValue("lv")); err != nil {
			return nil, err
		}
		s.audit(r, "storage", fmt.Sprintf("Deleted logical volume %s/%s", r.PathValue("vg"), r.PathValue("lv")))
		return nil, nil
	})

	// ---- Volumes (filesystems) ----
	s.user("GET /api/storage/volumes", func(r *http.Request) (any, error) {
		raids, _ := storage.ListRaid()
		var vols []store.Volume
		shareMap := map[string][]string{}
		s.store.Read(func(st *store.State) {
			for _, v := range st.Volumes {
				vols = append(vols, *v)
			}
			for _, sh := range st.Shares {
				for _, v := range st.Volumes {
					if sh.Path == v.MountPoint || strings.HasPrefix(sh.Path, v.MountPoint+"/") {
						shareMap[v.ID] = append(shareMap[v.ID], sh.Name)
					}
				}
			}
		})
		out := []volumeView{}
		for _, v := range vols {
			if live := storage.DevicesByUUID(v.UUID); len(live) > 0 {
				v.Devices = live
			}
			vv := volumeView{Volume: v, Mounted: storage.IsMountpoint(v.MountPoint), Shares: shareMap[v.ID], Health: "healthy"}
			if vv.Shares == nil {
				vv.Shares = []string{}
			}
			if vv.Mounted {
				vv.Usage, _ = storage.StatFS(v.MountPoint)
			} else {
				vv.Health = "unmounted"
			}
			for _, a := range raids {
				for _, d := range v.Devices {
					if d == a.Device || d == "/dev/md/"+a.Label || d == "/dev/md/"+a.Name {
						vv.Health = a.Health
					}
				}
			}
			out = append(out, vv)
		}
		return out, nil
	})
	s.admin("POST /api/storage/volumes", func(r *http.Request) (any, error) {
		var body struct {
			storage.CreateFSReq
			CacheDevice string `json:"cacheDevice"` // optional SSD cache
			CacheMode   string `json:"cacheMode"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		req := body.CreateFSReq
		if err := storage.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		if s.volumeNameTaken(req.Name) {
			return nil, badRequest("a volume with this name already exists")
		}
		if err := storage.ValidateFreeDisks(req.Devices, false); err != nil {
			return nil, badRequest(err.Error())
		}
		managed := ""
		if body.CacheDevice != "" {
			if len(req.Devices) != 1 {
				return nil, badRequest("an SSD cache needs a single data device: create a RAID array first for several disks")
			}
			if body.CacheMode == "" {
				body.CacheMode = "writethrough"
			}
			if body.CacheMode != "writethrough" && body.CacheMode != "writeback" {
				return nil, badRequest("invalid cache mode")
			}
			if err := storage.ValidateFreeDisks([]string{body.CacheDevice}, false); err != nil {
				return nil, badRequest(err.Error())
			}
			managed = "vol_" + req.Name
			if err := storage.ValidName(managed); err != nil {
				return nil, badRequest(err.Error())
			}
		}
		s.audit(r, "storage", fmt.Sprintf("Creating %s volume %s on %s", req.FSType, req.Name, strings.Join(req.Devices, ", ")))
		return s.jobs.Start("volume", "Create volume "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			if managed != "" {
				j.Logf("Groupe LVM %s avec cache SSD %s (%s)", managed, body.CacheDevice, body.CacheMode)
				lv, err := storage.CreateCachedLV(managed, req.Devices[0], body.CacheDevice, body.CacheMode)
				if err != nil {
					return err
				}
				req.Devices = []string{lv}
			}
			uuid, mp, err := storage.CreateFilesystem(ctx, req, j.Logf)
			if err != nil {
				if managed != "" {
					_ = storage.RemoveManagedVG(managed)
				}
				return err
			}
			profile := ""
			if req.FSType == "btrfs" {
				profile = req.DataProfile
			}
			return s.store.Update(func(st *store.State) error {
				st.Volumes = append(st.Volumes, &store.Volume{ID: shortID(), Name: req.Name, Devices: req.Devices, FSType: req.FSType, UUID: uuid, MountPoint: mp, Profile: profile, ManagedVG: managed, CreatedAt: time.Now()})
				return nil
			})
		}), nil
	})
	s.admin("POST /api/storage/volumes/import", func(r *http.Request) (any, error) {
		var req struct {
			Name   string `json:"name"`
			Device string `json:"device"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if s.volumeNameTaken(req.Name) {
			return nil, badRequest("a volume with this name already exists")
		}
		fstype, uuid, mp, err := storage.MountExisting(req.Name, req.Device)
		if err != nil {
			return nil, err
		}
		s.audit(r, "storage", "Imported volume "+req.Name+" from "+req.Device)
		return nil, s.store.Update(func(st *store.State) error {
			st.Volumes = append(st.Volumes, &store.Volume{ID: shortID(), Name: req.Name, Devices: []string{req.Device}, FSType: fstype, UUID: uuid, MountPoint: mp, CreatedAt: time.Now()})
			return nil
		})
	})
	s.admin("POST /api/storage/volumes/{id}/mount", func(r *http.Request) (any, error) {
		v, err := s.volume(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		if storage.IsMountpoint(v.MountPoint) {
			return nil, nil
		}
		_, _, _, err = storage.MountExisting(v.Name, storage.FindByUUID(v.UUID))
		return nil, err
	})
	s.admin("DELETE /api/storage/volumes/{id}", func(r *http.Request) (any, error) {
		v, err := s.volume(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		wipe := r.URL.Query().Get("wipe") == "1"
		var used []string
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				if sh.Path == v.MountPoint || strings.HasPrefix(sh.Path, v.MountPoint+"/") {
					used = append(used, sh.Name)
				}
			}
		})
		if len(used) > 0 {
			return nil, badRequest("volume is used by shares: " + strings.Join(used, ", "))
		}
		if v.FSType == "btrfs" && storage.BtrfsSnapshotsLocked(v.MountPoint) {
			return nil, badRequest("this volume holds locked snapshots: it cannot be deleted before they expire")
		}
		// Resolve the devices before unmounting, from the filesystem UUID (never from stored names).
		devices := storage.DevicesByUUID(v.UUID)
		if err := storage.UnmountVolume(v.MountPoint); err != nil {
			return nil, err
		}
		if wipe && v.ManagedVG != "" {
			// The LVM group made for the volume goes, freeing the disks and the SSD.
			if err := storage.RemoveManagedVG(v.ManagedVG); err != nil {
				return nil, err
			}
		} else if wipe {
			storage.Lock.Lock()
			for _, d := range devices {
				if err := storage.Wipe(d); err != nil {
					// md / LV devices are not "disks": wipe signatures only.
					_ = wipeSignatures(d)
				}
			}
			storage.Lock.Unlock()
		}
		s.audit(r, "storage", "Deleted volume "+v.Name)
		return nil, s.store.Update(func(st *store.State) error {
			for i, x := range st.Volumes {
				if x.ID == v.ID {
					st.Volumes = append(st.Volumes[:i], st.Volumes[i+1:]...)
					break
				}
			}
			return nil
		})
	})

	// ---- Btrfs ----
	s.admin("GET /api/storage/volumes/{id}/btrfs", func(r *http.Request) (any, error) {
		v, err := s.btrfsVolume(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		return storage.BtrfsDetail(v.MountPoint)
	})
	s.admin("POST /api/storage/volumes/{id}/btrfs/{action}", func(r *http.Request) (any, error) {
		v, err := s.btrfsVolume(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		var req struct {
			Name        string `json:"name"`
			Path        string `json:"path"`
			Device      string `json:"device"`
			DataProfile string `json:"dataProfile"`
			MetaProfile string `json:"metaProfile"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		action := r.PathValue("action")
		s.audit(r, "storage", fmt.Sprintf("Btrfs %s on %s %s%s", action, v.Name, req.Name, req.Device))
		switch action {
		case "subvolume":
			return nil, storage.BtrfsCreateSubvol(v.MountPoint, req.Name)
		case "delete-subvolume":
			return nil, storage.BtrfsDeleteSubvol(v.MountPoint, req.Path)
		case "snapshot":
			p, err := storage.BtrfsSnapshot(v.MountPoint, req.Path, req.Name)
			return map[string]string{"path": p}, err
		case "scrub":
			return nil, storage.BtrfsScrub(v.MountPoint)
		case "add-device":
			if err := storage.BtrfsAddDevice(v.MountPoint, req.Device); err != nil {
				return nil, err
			}
			return nil, s.store.Update(func(st *store.State) error {
				if x := st.Volume(v.ID); x != nil {
					x.Devices = append(x.Devices, req.Device)
				}
				return nil
			})
		case "remove-device":
			return s.jobs.Start("btrfs", "Remove "+req.Device+" from "+v.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
				if err := storage.BtrfsRemoveDevice(ctx, v.MountPoint, req.Device); err != nil {
					return err
				}
				return s.store.Update(func(st *store.State) error {
					if x := st.Volume(v.ID); x != nil {
						var keep []string
						for _, d := range x.Devices {
							if d != req.Device {
								keep = append(keep, d)
							}
						}
						x.Devices = keep
					}
					return nil
				})
			}), nil
		case "balance":
			return s.jobs.Start("btrfs", "Balance "+v.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
				return storage.BtrfsBalance(ctx, v.MountPoint, req.DataProfile, req.MetaProfile, j.Logf)
			}), nil
		}
		return nil, badRequest("unknown action")
	})

	// ---- ZFS ----
	s.admin("GET /api/storage/zfs", func(r *http.Request) (any, error) {
		pools, err := storage.ListZPools(true)
		if err != nil {
			return nil, err
		}
		return map[string]any{"available": storage.ZFSAvailable(), "broken": storage.ZFSBroken(), "pools": pools}, nil
	})
	s.admin("GET /api/storage/zfs/importable", func(r *http.Request) (any, error) { return storage.ListImportable() })
	s.admin("POST /api/storage/zfs/import", func(r *http.Request) (any, error) {
		var req struct{ Name string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return nil, storage.ImportZPool(req.Name)
	})
	s.admin("POST /api/storage/zfs", func(r *http.Request) (any, error) {
		var req storage.CreatePoolReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := storage.ValidName(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "storage", "Creating ZFS pool "+req.Name)
		return s.jobs.Start("zfs", "Create ZFS pool "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			return storage.CreateZPool(ctx, req, j.Logf)
		}), nil
	})
	s.admin("DELETE /api/storage/zfs/{pool}", func(r *http.Request) (any, error) {
		pool := r.PathValue("pool")
		var used []string
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				if strings.HasPrefix(sh.Path, storage.MountRoot+"/"+pool+"/") || sh.Path == storage.MountRoot+"/"+pool {
					used = append(used, sh.Name)
				}
			}
		})
		if len(used) > 0 {
			return nil, badRequest("pool is used by shares: " + strings.Join(used, ", "))
		}
		if err := storage.DestroyZPool(pool); err != nil {
			return nil, err
		}
		s.audit(r, "storage", "Destroyed ZFS pool "+pool)
		return nil, nil
	})
	s.admin("POST /api/storage/zfs/{pool}/{action}", func(r *http.Request) (any, error) {
		var req struct {
			Device    string `json:"device"`
			NewDevice string `json:"newDevice"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		pool, action := r.PathValue("pool"), r.PathValue("action")
		s.audit(r, "storage", fmt.Sprintf("ZFS pool %s: %s %s", pool, action, req.Device))
		return nil, storage.ZPoolAction(pool, action, req.Device, req.NewDevice)
	})
	s.admin("GET /api/storage/zfs-snapshots", func(r *http.Request) (any, error) {
		if !storage.ZFSAvailable() {
			return []storage.ZDataset{}, nil
		}
		list, err := storage.ListZDatasets(r.URL.Query().Get("pool"), "snapshot")
		return storage.MarkLocks(list), err
	})
	s.admin("POST /api/storage/zfs-datasets", func(r *http.Request) (any, error) {
		var req storage.CreateDatasetReq
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		s.audit(r, "storage", "Created ZFS dataset "+req.Name)
		return nil, storage.CreateDataset(req)
	})
	s.admin("PUT /api/storage/zfs-datasets", func(r *http.Request) (any, error) {
		var req struct {
			Name  string            `json:"name"`
			Props map[string]string `json:"props"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return nil, storage.SetDatasetProps(req.Name, req.Props)
	})
	s.admin("DELETE /api/storage/zfs-datasets", func(r *http.Request) (any, error) {
		name := r.URL.Query().Get("name")
		s.audit(r, "storage", "Destroyed ZFS dataset "+name)
		return nil, storage.DestroyDataset(name, r.URL.Query().Get("recursive") == "1")
	})
	s.admin("POST /api/storage/zfs-snapshots", func(r *http.Request) (any, error) {
		var req struct {
			Dataset   string `json:"dataset"`
			Name      string `json:"name"`
			Recursive bool   `json:"recursive"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return nil, storage.Snapshot(req.Dataset, req.Name, req.Recursive)
	})
	s.admin("POST /api/storage/zfs-snapshots/rollback", func(r *http.Request) (any, error) {
		var req struct{ Snapshot string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		s.audit(r, "storage", "Rolled back to "+req.Snapshot)
		return nil, storage.Rollback(req.Snapshot)
	})

	// Locations a share can point to: volume roots, btrfs subvolumes and ZFS datasets.
	s.admin("GET /api/storage/locations", func(r *http.Request) (any, error) {
		type loc struct {
			Path  string `json:"path"`
			Label string `json:"label"`
			Free  int64  `json:"free"`
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
			out = append(out, loc{Path: v.MountPoint, Label: v.Name + " (" + v.FSType + ")", Free: u.Free})
		}
		if storage.ZFSAvailable() {
			ds, _ := storage.ListZDatasets("", "filesystem")
			for _, d := range ds {
				if strings.HasPrefix(d.Mountpoint, storage.MountRoot+"/") {
					out = append(out, loc{Path: d.Mountpoint, Label: d.Name + " (zfs)", Free: d.Avail})
				}
			}
		}
		return out, nil
	})
}

func (s *Server) volumeNameTaken(name string) bool {
	taken := false
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			if v.Name == name {
				taken = true
			}
		}
	})
	if pools, _ := storage.ListZPools(false); pools != nil {
		for _, p := range pools {
			if p.Name == name {
				taken = true
			}
		}
	}
	return taken
}

func (s *Server) volume(id string) (*store.Volume, error) {
	var v *store.Volume
	s.store.Read(func(st *store.State) {
		if x := st.Volume(id); x != nil {
			c := *x
			v = &c
		}
	})
	if v == nil {
		return nil, notFound("volume not found")
	}
	return v, nil
}

func (s *Server) btrfsVolume(id string) (*store.Volume, error) {
	v, err := s.volume(id)
	if err != nil {
		return nil, err
	}
	if v.FSType != "btrfs" {
		return nil, badRequest("not a btrfs volume")
	}
	return v, nil
}

func wipeSignatures(dev string) error {
	if err := storage.ValidateDevice(dev); err != nil {
		return err
	}
	_, err := runCmd("wipefs", "-a", dev)
	return err
}
