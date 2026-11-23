package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"strataos/internal/restore"
	"strataos/internal/storage"
)

// File versions read from the snapshots of the volume holding the file,
// shown beside the file in the file manager (roadmap 0.15, "File versions").

type fileVersion struct {
	Snapshot string    `json:"snapshot"`
	Time     time.Time `json:"time"` // when the snapshot was taken
	Size     int64     `json:"size"`
	Mtime    time.Time `json:"modTime"` // of the file inside the snapshot (same key as storage.Version)
}

// maxFileVersions caps the history shown beside a file.
const maxFileVersions = 50

// fileVersionSource returns the snapshot source of the volume holding abs.
func fileVersionSource(abs string) (restore.Source, error) {
	mp := storage.MountOf(abs)
	if mp == "" || !strings.HasPrefix(mp, storage.MountRoot+"/") || storage.ShadowMount(mp) != mp {
		return nil, notFound("no snapshots for this folder")
	}
	return restore.SnapshotSource(mp), nil
}

// versionsOf lists the versions of abs in the points, newest first, capped.
func versionsOf(ctx context.Context, src restore.Source, points []restore.Point, abs string) []fileVersion {
	out := []fileVersion{}
	for i := len(points) - 1; i >= 0 && len(out) < maxFileVersions; i-- {
		e, err := src.Stat(ctx, points[i].ID, abs)
		if err != nil || e.Dir {
			continue
		}
		out = append(out, fileVersion{Snapshot: points[i].ID, Time: points[i].Time, Size: e.Size, Mtime: e.MTime})
	}
	return out
}

func (s *Server) fileVersionsRoutes() {
	// GET /api/files/versions lives in versionRoutes (h_versions.go): one
	// handler for both the restore dialog and the file details panel.
	s.user("POST /api/files/restore-version", func(r *http.Request) (any, error) {
		var req struct {
			Path     string `json:"path"`
			Snapshot string `json:"snapshot"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		t, err := s.locate(r, req.Path)
		if err != nil {
			return nil, err
		}
		if !t.acc.write {
			return nil, &Error{Status: http.StatusForbidden, Msg: "this folder is read-only for you"}
		}
		src, err := fileVersionSource(t.abs)
		if err != nil {
			return nil, err
		}
		pts, err := src.Points(r.Context())
		if err != nil {
			return nil, err
		}
		found := false
		for _, p := range pts {
			if p.ID == req.Snapshot {
				found = true
				break
			}
		}
		if !found {
			return nil, notFound("snapshot not found")
		}
		// A safety snapshot before overwriting, like the guided restore.
		safetySnapshot(filepath.Dir(t.abs), func(string, ...any) {})
		tmp := t.abs + ".strata-restore"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, err
		}
		derr := src.Dump(r.Context(), req.Snapshot, t.abs, f)
		f.Close()
		if derr != nil {
			_ = os.Remove(tmp)
			return nil, derr
		}
		mode := os.FileMode(0o644)
		if info, err := os.Stat(t.abs); err == nil {
			mode = info.Mode().Perm()
		}
		if err := os.Chmod(tmp, mode); err != nil {
			_ = os.Remove(tmp)
			return nil, err
		}
		if err := os.Rename(tmp, t.abs); err != nil {
			_ = os.Remove(tmp)
			return nil, err
		}
		s.audit(r, "files", "Restored "+req.Path+" from snapshot "+req.Snapshot)
		return map[string]bool{"ok": true}, nil
	})
}
