package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"strataos/internal/photos"
	"strataos/internal/sys"
)

// maxEditPhotoBytes bounds the size of a picture the light editor decodes.
const maxEditPhotoBytes = 64 << 20

// editPhoto rewrites a JPEG picture in place (rotation, brightness, auto
// light), atomically and keeping its permissions; snapshots keep the previous
// versions. t must already be located and writable.
func (s *Server) editPhoto(t target, rotate, brightness int, autoLight bool) error {
	ext := strings.ToLower(filepath.Ext(t.abs))
	if ext != ".jpg" && ext != ".jpeg" {
		return badRequest("only JPEG pictures can be edited here")
	}
	info, err := os.Stat(t.abs)
	if err != nil {
		return notFound("file not found")
	}
	if info.Size() > maxEditPhotoBytes {
		return badRequest("picture too large to edit here")
	}
	src, err := os.ReadFile(t.abs)
	if err != nil {
		return err
	}
	out, err := photos.EditJPEG(src, rotate, brightness, autoLight)
	if err != nil {
		return badRequest(err.Error())
	}
	return sys.WriteFileAtomic(t.abs, out, info.Mode().Perm())
}

// photoEditRoutes: the light editor of the gallery (roadmap 0.15).
func (s *Server) photoEditRoutes() {
	s.user("POST /api/photos/edit", func(r *http.Request) (any, error) {
		var req struct {
			Path       string `json:"path"`
			Rotate     int    `json:"rotate"`     // degrees, multiple of 90
			Brightness int    `json:"brightness"` // -100..100
			AutoLight  bool   `json:"autoLight"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Rotate == 0 && req.Brightness == 0 && !req.AutoLight {
			return nil, badRequest("nothing to do")
		}
		if req.Rotate%90 != 0 || req.Rotate < -270 || req.Rotate > 270 {
			return nil, badRequest("rotation must be a multiple of 90° between -270 and 270")
		}
		if req.Brightness < -100 || req.Brightness > 100 {
			return nil, badRequest("brightness must be between -100 and 100")
		}
		t, err := s.locate(r, req.Path)
		if err != nil {
			return nil, err
		}
		if !t.acc.write {
			return nil, &Error{Status: http.StatusForbidden, Msg: "this folder is read-only for you"}
		}
		if err := s.editPhoto(t, req.Rotate, req.Brightness, req.AutoLight); err != nil {
			return nil, err
		}
		s.audit(r, "files", "Photo edited: "+req.Path)
		return map[string]bool{"ok": true}, nil
	})
}
