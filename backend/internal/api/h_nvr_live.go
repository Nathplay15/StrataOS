package api

import (
	"net/http"
	"time"

	"strataos/internal/nvr"
)

// nvrLiveRoutes: the live video of a camera as fragmented MP4 (stream copy of
// the camera's H.264 or H.265), shared between viewers (nvr/livestream.go).
// Players read it as a progressive file that never ends: Media3 in the app,
// a <video> element in the browser.
func (s *Server) nvrLiveRoutes() {
	s.adminRaw("GET /api/nvr/cameras/{id}/live.mp4", func(w http.ResponseWriter, r *http.Request) {
		c, _, err := s.nvrCamera(r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		if !c.Enabled {
			writeErr(w, badRequest("the camera is disabled"))
			return
		}
		if !nvr.Installed() {
			writeErr(w, badRequest("ffmpeg is not installed"))
			return
		}
		kind := r.URL.Query().Get("stream")
		switch {
		case kind == "" && c.SubURL != "":
			kind = "sub"
		case kind == "", kind == "main":
			kind = "main"
		case kind == "sub":
			if c.SubURL == "" {
				writeErr(w, badRequest("no sub-stream"))
				return
			}
		default:
			writeErr(w, badRequest("the stream must be main or sub"))
			return
		}
		stream := c.URL
		if kind == "sub" {
			stream = c.SubURL
		}
		// MJPEG and the other codecs of HTTP cameras do not fit in MP4:
		// those keep the live pictures.
		if nvr.HTTPStream(stream) {
			writeErr(w, &Error{Status: http.StatusUnsupportedMediaType, Msg: "live video needs an RTSP camera: use the live pictures"})
			return
		}
		v, err := cams.mgr.LiveStream(c.ID, kind, nvr.WithCredentials(stream, c.Username, c.Password), c.Audio, 15*time.Second)
		if err != nil {
			writeErr(w, &Error{Status: http.StatusBadGateway, Msg: err.Error()})
			return
		}
		defer cams.mgr.Leave(v)
		h := w.Header()
		h.Set("Content-Type", "video/mp4")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		if _, err := w.Write(v.Init); err != nil {
			return
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case f, ok := <-v.C:
				if !ok {
					return
				}
				// A viewer that cannot keep up is dropped rather than slowing
				// ffmpeg down for the others.
				_ = rc.SetWriteDeadline(time.Now().Add(20 * time.Second))
				if _, err := w.Write(f.Data); err != nil {
					return
				}
				if rc.Flush() != nil {
					return
				}
			}
		}
	})
}
