package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strataos/internal/store"
	"strataos/internal/sys"
)

func TestSelfCheck(t *testing.T) {
	ok := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Write([]byte(`<div id="root"></div>`))
		}
	}))
	defer ok.Close()
	if p := selfCheck(ok.URL, nil); p != "" {
		t.Fatalf("healthy: %q", p)
	}
}

// After a rollback, the previous version tells the administrators.
func TestUpdateSelfCheckRolledBack(t *testing.T) {
	s := newTestServer(t)
	checkRun = func(string, ...string) (string, error) { return "", nil }
	defer func() { checkRun = sys.Run }()
	updateCheckFile = filepath.Join(t.TempDir(), "update-check.env")
	os.WriteFile(updateCheckFile, []byte("FROM=0.15.3\nTO=0.15.4\nSERVICES=\nROLLEDBACK=1\n"), 0o600)
	s.version = "0.15.3"
	s.updateSelfCheck("https://127.0.0.1:1", 0)
	if _, err := os.Stat(updateCheckFile); err == nil {
		t.Fatal("marker kept")
	}
	var last string
	s.store.Read(func(st *store.State) { last = st.Events[len(st.Events)-1].Message })
	if !strings.Contains(last, "0.15.3 was installed again") {
		t.Fatalf("logged %q", last)
	}
}
