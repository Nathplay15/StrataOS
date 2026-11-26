package api

import (
	"net/http"
	"net/http/httptest"
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
			"label": "Share docs", "reason": "StrataOS update", "n": 12345678901234}, nil
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
	if b := get("/api/t/job", "fr-FR,fr;q=0.9"); !strings.Contains(b, `"title":"Mise à jour de StrataOS"`) || !strings.Contains(b, `"reason":"Mise à jour de StrataOS"`) || !strings.Contains(b, `"label":"Share docs"`) || !strings.Contains(b, "12345678901234") {
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
