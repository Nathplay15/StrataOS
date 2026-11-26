package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"strataos/internal/system"
)

// releaseNotes keeps the notes fetched, by version.
var releaseNotes = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

// fetchNotes reads the release notes of version from the StrataOS
// repository (make-repo.sh publishes packaging/release-notes there).
func fetchNotes(ctx context.Context, base, version string) (string, error) {
	releaseNotes.Lock()
	if n, ok := releaseNotes.m[version]; ok {
		releaseNotes.Unlock()
		return n, nil
	}
	releaseNotes.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/"+system.NotesName(version), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", &Error{Status: http.StatusBadGateway, Msg: "the repository answered " + resp.Status}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	n := strings.ToValidUTF8(string(b), "")
	releaseNotes.Lock()
	releaseNotes.m[version] = n
	releaseNotes.Unlock()
	return n, nil
}

func (s *Server) notesRoutes() {
	// The notes of the version an update would install (none: "").
	s.admin("GET /api/system/updates/strata/notes", func(r *http.Request) (any, error) {
		_, candidate := strataPolicy()
		base := system.StrataRepoURL()
		if candidate == "" || candidate == "(none)" || base == "" || !strings.HasPrefix(base, "http") {
			return map[string]string{"version": candidate, "notes": ""}, nil
		}
		n, err := fetchNotes(r.Context(), base, candidate)
		if err != nil {
			return nil, err
		}
		return map[string]string{"version": candidate, "notes": n}, nil
	})
}
