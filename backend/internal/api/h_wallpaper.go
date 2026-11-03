package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif" // decoders registered for image.Decode
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"strataos/internal/store"
	"strataos/internal/sys"
)

// Wallpaper image uploaded by a user for the desktop mode. The upload is
// decoded and re-encoded as a JPEG, so what is stored and served back is
// always a plain image, whatever the browser sent.

const (
	maxWallpaperBytes  = 5 << 20
	maxWallpaperSide   = 8192
	maxWallpaperPixels = 40_000_000
)

// wallpaperPath is where the user's image lives; the name is a hash so any
// account name maps to a safe file name.
func (s *Server) wallpaperPath(user string) string {
	h := sha256.Sum256([]byte(user))
	return filepath.Join(s.dataDir, "wallpapers", hex.EncodeToString(h[:16])+".jpg")
}

// normalizeWallpaper checks an uploaded image (JPEG, PNG or GIF, bounded
// size and dimensions, checked before decoding) and re-encodes it as JPEG.
func normalizeWallpaper(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty image")
	}
	if len(data) > maxWallpaperBytes {
		return nil, fmt.Errorf("the image is larger than 5 MB")
	}
	switch http.DetectContentType(data) {
	case "image/jpeg", "image/png", "image/gif":
	default:
		return nil, fmt.Errorf("the wallpaper must be a JPEG, PNG or GIF image")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("unreadable image")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxWallpaperSide || cfg.Height > maxWallpaperSide || cfg.Width*cfg.Height > maxWallpaperPixels {
		return nil, fmt.Errorf("the image is too large (8192 × 8192 pixels at most)")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("unreadable image")
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (s *Server) wallpaperRoutes() {
	s.user("PUT /api/me/wallpaper", func(r *http.Request) (any, error) {
		data, err := io.ReadAll(io.LimitReader(r.Body, maxWallpaperBytes+1))
		if err != nil {
			return nil, err
		}
		img, err := normalizeWallpaper(data)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		p := s.wallpaperPath(userOf(r))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil, err
		}
		if err := sys.WriteFileAtomic(p, img, 0o600); err != nil {
			return nil, err
		}
		return nil, s.store.Update(func(st *store.State) error {
			if u := st.User(userOf(r)); u != nil {
				u.Wallpaper = "custom"
			}
			return nil
		})
	})

	s.userRaw("GET /api/me/wallpaper", func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open(s.wallpaperPath(userOf(r)))
		if err != nil {
			writeErr(w, notFound("no wallpaper"))
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			writeErr(w, err)
			return
		}
		// Always served as a JPEG that cannot be sniffed into anything else.
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Cache-Control", "private, max-age=86400")
		http.ServeContent(w, r, "", fi.ModTime(), f)
	})

	s.user("DELETE /api/me/wallpaper", func(r *http.Request) (any, error) {
		if err := os.Remove(s.wallpaperPath(userOf(r))); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return nil, s.store.Update(func(st *store.State) error {
			if u := st.User(userOf(r)); u != nil && u.Wallpaper == "custom" {
				u.Wallpaper = ""
			}
			return nil
		})
	})
}
