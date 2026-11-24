package api

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"strataos/internal/photos"
	"strataos/internal/rootio"
)

// maxEditPhotoBytes bounds the size of a picture the light editor decodes.
const maxEditPhotoBytes = 64 << 20

// editPhoto rewrites a JPEG picture in place (rotation, crop, brightness,
// auto light) through the share's root: a symlink is never followed out of
// it, the new picture replaces the file atomically and keeps its mode and
// owner; snapshots keep the previous versions. t must already be located
// and writable.
func (s *Server) editPhoto(t target, e photos.Edit) error {
	ext := strings.ToLower(filepath.Ext(t.abs))
	if ext != ".jpg" && ext != ".jpeg" {
		return badRequest("only JPEG pictures can be edited here")
	}
	rt, err := t.open()
	if err != nil {
		return err
	}
	defer rt.Close()
	src, err := rootio.ReadFile(rt, t.rel, maxEditPhotoBytes)
	if errors.Is(err, rootio.ErrTooLarge) {
		return badRequest("picture too large to edit here")
	} else if err != nil {
		return notFound("file not found")
	}
	out, err := photos.EditJPEG(src, e)
	if err != nil {
		return badRequest(err.Error())
	}
	owner, perm, ok := rootio.OwnerOf(rt, t.rel)
	if !ok {
		// The file is a link inside the share: replace it by the picture.
		owner, perm = rootio.NoOwner, 0o644
	}
	return rootio.WriteFile(rt, t.rel, out, perm, owner)
}

// photoEditRoutes: the light editor of the gallery (roadmap 0.15).
func (s *Server) photoEditRoutes() {
	s.user("POST /api/photos/edit", func(r *http.Request) (any, error) {
		var req struct {
			Path       string       `json:"path"`
			Rotate     int          `json:"rotate"`     // degrees, multiple of 90
			Brightness int          `json:"brightness"` // -100..100
			AutoLight  bool         `json:"autoLight"`
			Crop       *photos.Crop `json:"crop"` // fractions of the rotated picture
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Rotate == 0 && req.Brightness == 0 && !req.AutoLight && req.Crop == nil {
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
		if err := s.editPhoto(t, photos.Edit{Rotate: req.Rotate, Brightness: req.Brightness, AutoLight: req.AutoLight, Crop: req.Crop}); err != nil {
			return nil, err
		}
		s.audit(r, "files", "Photo edited: "+req.Path)
		return map[string]bool{"ok": true}, nil
	})
}
