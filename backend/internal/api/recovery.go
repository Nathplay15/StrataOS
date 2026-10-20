package api

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// recoverMu serialises recovery passes (startup and periodic health checks).
var recoverMu sync.Mutex

// recoverStorage brings storage back after a boot with missing or late disks:
// it starts inactive RAID arrays degraded when enough members remain, mounts
// volumes whose filesystem is present but not mounted, and restarts NFS when it
// failed because an exported folder was missing at boot.
func (s *Server) recoverStorage() {
	recoverMu.Lock()
	defer recoverMu.Unlock()
	changed := false

	if arrays, err := storage.ListRaid(); err == nil {
		for _, a := range arrays {
			if a.Health != "inactive" {
				continue
			}
			name := firstNonEmpty(a.Label, a.Name)
			if err := storage.StartDegraded(a); err != nil {
				msg := fmt.Sprintf("RAID array %s cannot start: too many disks missing (%v)", name, err)
				if s.firstTime(msg) {
					s.store.Log("error", "health", "", msg)
					s.notify("error", "RAID unavailable", msg)
				}
				continue
			}
			changed = true
			msg := fmt.Sprintf("RAID array %s started degraded: a disk is missing, replace it soon", name)
			s.store.Log("warning", "health", "", msg)
			s.notify("error", "RAID degraded", msg)
		}
	}
	if changed {
		_, _ = sys.Run("udevadm", "settle")
	}

	var vols []store.Volume
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			vols = append(vols, *v)
		}
	})
	for _, v := range vols {
		if storage.IsMountpoint(v.MountPoint) {
			storage.EnsureQuotaOn(v.MountPoint, v.FSType)
			continue
		}
		if len(storage.DevicesByUUID(v.UUID)) == 0 {
			continue
		}
		// Go through systemd: it may be mounting the same fstab entry right now, as the
		// device just appeared; starting its unit is idempotent, a raw mount is not.
		unit, err := sys.Run("systemd-escape", "--path", "--suffix=mount", v.MountPoint)
		if err == nil {
			_, err = sys.Run("systemctl", "start", strings.TrimSpace(unit))
		}
		if err != nil {
			slog.Warn("recovery mount failed", "volume", v.Name, "err", err)
			continue
		}
		changed = true
		s.store.Log("info", "health", "", "Volume "+v.Name+" remounted automatically")
	}

	if changed || unitFailed("nfs-server.service") {
		var nfs bool
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				nfs = nfs || sh.NFS
			}
		})
		if nfs {
			_, _ = sys.Run("systemctl", "restart", "nfs-server.service")
		}
		if changed {
			_, _ = sys.Run("systemctl", "reload-or-restart", "smbd.service")
		}
	}
}

func unitFailed(unit string) bool {
	out, _ := sys.Run("systemctl", "is-failed", unit)
	return strings.TrimSpace(out) == "failed"
}

var seenMsgs sync.Map

// firstTime reports whether msg has not been seen since the daemon started.
func (s *Server) firstTime(msg string) bool {
	_, seen := seenMsgs.LoadOrStore(msg, true)
	return !seen
}
