// Full-text search of file contents: the index (internal/ftsearch) covers
// every share and is refreshed in the background; the routes only return what
// the calling user may read.

package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"strataos/internal/ftsearch"
	"strataos/internal/jobs"
	"strataos/internal/store"
)

func (s *Server) fulltextRoutes() {
	s.user("GET /api/files/fulltext", func(r *http.Request) (any, error) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if len([]rune(q)) < 2 || len(q) > 200 {
			return nil, badRequest("search for at least 2 characters")
		}
		limit := qInt(r, "limit", 20)
		if limit < 1 || limit > 100 {
			limit = 20
		}
		terms := ftsearch.Tokenize(q)
		if len(terms) == 0 {
			return nil, badRequest("search for at least 2 characters")
		}
		// Overfetch: hits the user may not read are dropped below.
		hits := ftsearch.Default.Search(terms, limit*4)
		roots := s.readableRoots(r)
		if p := r.URL.Query().Get("path"); p != "" {
			t, err := s.locate(r, p)
			if err != nil {
				return nil, err
			}
			roots = []string{t.abs}
		}
		out := make([]fulltextHit, 0, limit)
		truncated := false
		for _, h := range hits {
			if !under(roots, h.Path) {
				continue
			}
			out = append(out, fulltextHit{
				Path: h.Path, Name: h.Name, Size: h.Size, ModTime: time.Unix(h.Mtime, 0),
				Score: h.Score, Snippet: ftsearch.Default.Snippet(h.Path, terms),
			})
			if len(out) >= limit {
				break
			}
		}
		if len(hits) > limit*3 {
			truncated = true
		}
		docs, updated := ftsearch.Default.Stats()
		return map[string]any{"query": q, "hits": out, "truncated": truncated, "docs": docs, "updated": updated, "running": ftsearch.Default.Running()}, nil
	})

	s.admin("POST /api/files/fulltext/reindex", func(r *http.Request) (any, error) {
		if ftsearch.Default.Running() {
			return nil, &Error{Status: http.StatusConflict, Msg: "indexing already running"}
		}
		return s.jobs.Start("fulltext", "Full-text indexing", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			_, err := ftsearch.Default.Scan(ctx, s.fulltextRoots(), j.Logf)
			return err
		}), nil
	})
}

// fulltextHit is one content match, with an extract of the document.
type fulltextHit struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	Score   int       `json:"score"`
	Snippet string    `json:"snippet"`
}

// fulltextRoots returns every folder to index: all shares (administrators
// search everything anyway, and other users are filtered at query time).
func (s *Server) fulltextRoots() []string {
	var roots []string
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			roots = append(roots, sh.Path)
		}
	})
	return roots
}

// fulltextLoop loads the index and refreshes it every 30 minutes.
func (s *Server) fulltextLoop() {
	ftsearch.Default.Load()
	time.Sleep(2 * time.Minute)
	for {
		if roots := s.fulltextRoots(); len(roots) > 0 && !ftsearch.Default.Running() {
			if _, err := ftsearch.Default.Scan(context.Background(), roots, func(string, ...any) {}); err != nil {
				slog.Warn("fulltext indexing", "err", err)
			}
		}
		time.Sleep(30 * time.Minute)
	}
}
