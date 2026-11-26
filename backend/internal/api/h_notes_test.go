package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"strataos/internal/system"
)

func TestFetchNotes(t *testing.T) {
	if got := system.NotesName("0.8.0~beta.1"); got != "notes/0.8.0-beta.1.md" {
		t.Fatalf("NotesName = %q", got)
	}
	if got := system.NotesName("0.15.4-1"); got != "notes/0.15.4.md" {
		t.Fatalf("NotesName = %q", got)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apt/notes/9.9.9.md" {
			w.Write([]byte("StrataOS 9.9.9: better."))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if n, err := fetchNotes(context.Background(), srv.URL+"/apt", "9.9.9"); err != nil || n != "StrataOS 9.9.9: better." {
		t.Fatalf("notes %q %v", n, err)
	}
	if n, err := fetchNotes(context.Background(), srv.URL+"/apt", "9.9.8"); err != nil || n != "" {
		t.Fatalf("missing notes %q %v", n, err)
	}
}
