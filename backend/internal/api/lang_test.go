package api

import (
	"time"

	"net/http"
	"net/http/httptest"
	"strataos/internal/events"
	"strings"
	"testing"
)

// The API renders its errors, reasons and job titles in the language of
// Accept-Language; names and unknown sentences are left as they are.
func TestResponsesInCallerLanguage(t *testing.T) {
	s := newTestServer(t)
	s.public("GET /api/t/err", func(r *http.Request) (any, error) { return nil, badRequest("Update failed because the disk is full") })
	s.public("GET /api/t/job", func(r *http.Request) (any, error) {
		return map[string]any{"jobs": []any{map[string]any{"title": "StrataOS update", "status": "running", "progress": 1}},
			"label": "Share docs", "reason": "StrataOS update", "n": 12345678901234,
			"items": []any{map[string]any{"subject": "StrataOS update undone", "message": "a user wrote this"}}}, nil
	})
	get := func(path, lang string) string {
		req := httptest.NewRequest("GET", path, nil)
		if lang != "" {
			req.Header.Set("Accept-Language", lang)
		}
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)
		return w.Body.String()
	}
	if b := get("/api/t/job", "fr-FR,fr;q=0.9"); !strings.Contains(b, `"title":"Mise à jour de StrataOS"`) || !strings.Contains(b, `"reason":"Mise à jour de StrataOS"`) || !strings.Contains(b, `"label":"Share docs"`) || !strings.Contains(b, "12345678901234") ||
		!strings.Contains(b, `"subject":"Mise à jour de StrataOS annulée"`) || !strings.Contains(b, `"message":"a user wrote this"`) {
		t.Fatalf("fr: %s", b)
	}
	if b := get("/api/t/job", "de"); strings.Contains(b, `"title":"StrataOS update"`) {
		t.Fatalf("de not applied: %s", b)
	}
	if b := get("/api/t/job", ""); !strings.Contains(b, `"title":"StrataOS update"`) {
		t.Fatalf("en: %s", b)
	}
	if b := get("/api/t/err", "fr"); !strings.Contains(b, "Update failed because the disk is full") {
		t.Fatalf("unknown sentence changed: %s", b)
	}
}

// A component change makes the GPU detected again (events → caches.go).
func TestComponentEventForgetsGPU(t *testing.T) {
	s := newTestServer(t)
	s.subscribeCaches()
	gpuCache.Lock()
	gpuCache.at = time.Now()
	gpuCache.Unlock()
	events.Publish(events.Components, "media")
	gpuCache.Lock()
	defer gpuCache.Unlock()
	if !gpuCache.at.IsZero() {
		t.Fatal("GPU cache kept after a component install")
	}
}
