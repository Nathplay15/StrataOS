package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"strataos-sync/internal/api"
	"strataos-sync/internal/config"
	"strataos-sync/internal/engine"
)

const testToken = "0123456789abcdef0123456789abcdef0123"

// fakeNAS answers like a NAS: it records what reaches it.
type fakeNAS struct {
	*httptest.Server
	mu   sync.Mutex
	seen []*http.Request
}

func newFakeNAS(t *testing.T) *fakeNAS {
	f := &fakeNAS{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/mobile/websession", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer stdev_x" {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `{"code":"c1","path":"/api/mobile/websession/c1"}`)
	})
	mux.HandleFunc("/api/mobile/websession/c1", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "strata_session", Value: "sess1", Path: "/", Secure: true, HttpOnly: true})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, r.Clone(context.Background()))
		f.mu.Unlock()
		w.Header().Set("Set-Cookie", "other=1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"path":"`+r.URL.Path+`"}`)
	})
	f.Server = httptest.NewTLSServer(mux)
	t.Cleanup(f.Close)
	return f
}

// lastTo is the latest request to path: the agent's own background calls
// (device check, events) may come after the one a test made.
func (f *fakeNAS) lastTo(path string) *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.seen) - 1; i >= 0; i-- {
		if f.seen[i].URL.Path == path {
			return f.seen[i]
		}
	}
	return nil
}

// newTestAgent runs an agent whose main account is paired with nas.
func newTestAgent(t *testing.T, nas *fakeNAS, pairs []engine.Pair) (*Agent, string) {
	root := t.TempDir()
	if nas != nil {
		c := config.Config{Server: nas.URL, Token: "stdev_x", Fingerprint: api.Fingerprint(nas.Certificate().Raw), NASName: "nas", User: "alice", Pairs: pairs}
		if err := (&config.Store{Dir: root}).Save(c); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	a, err := New(ctx, root, "test", testToken)
	if err != nil {
		t.Fatal(err)
	}
	port, err := a.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		a.Stop()
	})
	return a, "http://127.0.0.1:" + strconv.Itoa(port)
}

func do(t *testing.T, method, u, origin string, auth bool) *http.Response {
	t.Helper()
	r, _ := http.NewRequest(method, u, nil)
	if auth {
		r.Header.Set("Authorization", "Bearer "+testToken)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestAuth(t *testing.T) {
	_, base := newTestAgent(t, nil, nil)
	if r := do(t, "GET", base+"/v1/state", "", false); r.StatusCode != 403 {
		t.Fatalf("no token: %d", r.StatusCode)
	}
	if r := do(t, "GET", base+"/v1/state", "", true); r.StatusCode != 200 {
		t.Fatalf("token: %d", r.StatusCode)
	}
	if r := do(t, "GET", base+"/v1/state?st="+testToken, "", false); r.StatusCode != 200 {
		t.Fatalf("token in the address (GET): %d", r.StatusCode)
	}
	if r := do(t, "POST", base+"/v1/sync-now?st="+testToken, "", false); r.StatusCode != 403 {
		t.Fatalf("token in the address (POST): %d", r.StatusCode)
	}
	if r := do(t, "GET", base+"/v1/state", "https://evil.example", true); r.StatusCode != 403 {
		t.Fatalf("other origin: %d", r.StatusCode)
	}
	r := do(t, "OPTIONS", base+"/v1/state", "tauri://localhost", false)
	if r.StatusCode != 204 || r.Header.Get("Access-Control-Allow-Origin") != "tauri://localhost" {
		t.Fatalf("preflight: %d %v", r.StatusCode, r.Header)
	}
	if r := do(t, "GET", base+"/v1/state", "http://tauri.localhost", true); r.StatusCode != 200 || r.Header.Get("Access-Control-Allow-Origin") == "" {
		t.Fatalf("app origin: %d", r.StatusCode)
	}
	// Another host name (DNS rebinding) is refused even with the token.
	req, _ := http.NewRequest("GET", base+"/v1/state", nil)
	req.Host = "rebind.example:80"
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || resp.StatusCode != 403 {
		t.Fatalf("other host: %v %v", resp.StatusCode, err)
	}
	if _, err := New(context.Background(), t.TempDir(), "x", "short"); err == nil {
		t.Fatal("short token accepted")
	}
}

func TestNASProxy(t *testing.T) {
	nas := newFakeNAS(t)
	_, base := newTestAgent(t, nas, nil)
	r := do(t, "GET", base+"/v1/accounts/main/nas/api/files?path=%2Fsrv&st="+testToken, "tauri://localhost", false)
	if r.StatusCode != 200 {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("proxy: %d %s", r.StatusCode, b)
	}
	if r.Header.Get("Set-Cookie") != "" {
		t.Fatal("cookie of the NAS passed to the window")
	}
	seen := nas.lastTo("/api/files")
	if seen == nil || seen.URL.Query().Get("path") != "/srv" || seen.URL.Query().Has("st") {
		t.Fatalf("request: %v", seen.URL)
	}
	if seen.Header.Get("Authorization") != "Bearer stdev_x" || seen.Header.Get("Origin") != "" || seen.Header.Get("X-Strata") != "1" {
		t.Fatalf("headers: %v", seen.Header)
	}
	if r := do(t, "GET", base+"/v1/accounts/main/nas/other", "", true); r.StatusCode != 403 {
		t.Fatalf("not the API: %d", r.StatusCode)
	}
	if r := do(t, "GET", base+"/v1/accounts/nope/nas/api/x", "", true); r.StatusCode != 404 {
		t.Fatalf("unknown account: %d", r.StatusCode)
	}
}

func TestWebProxy(t *testing.T) {
	nas := newFakeNAS(t)
	a, _ := newTestAgent(t, nas, nil)
	x, _ := a.account(MainAccount)
	u, err := a.openWeb(x, "/storage")
	if err != nil {
		t.Fatal(err)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/storage" {
		t.Fatalf("enter: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	gate := resp.Cookies()
	if len(gate) != 1 || !strings.HasPrefix(gate[0].Name, "strataos_web_") {
		t.Fatalf("cookies given to the webview: %v", gate)
	}
	// The one-time key works once.
	if r, _ := noRedirect.Get(u); r.StatusCode != 403 {
		t.Fatalf("key used twice: %d", r.StatusCode)
	}
	root := strings.SplitN(u, "/__strataos", 2)[0]
	req, _ := http.NewRequest("POST", root+"/api/x", strings.NewReader("{}"))
	req.Header.Set("Origin", root)
	req.Header.Set("Referer", root+"/storage")
	req.AddCookie(gate[0])
	r, err := noRedirect.Do(req)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("proxied: %v %v", r, err)
	}
	r.Body.Close()
	if r.Header.Get("Set-Cookie") != "" {
		t.Fatal("NAS cookie passed to the webview")
	}
	seen := nas.lastTo("/api/x")
	if seen == nil {
		t.Fatal("request not proxied")
	}
	if c, err := seen.Cookie("strata_session"); err != nil || c.Value != "sess1" {
		t.Fatalf("session not attached: %v", seen.Header)
	}
	if _, err := seen.Cookie(gate[0].Name); err == nil {
		t.Fatal("proxy cookie passed to the NAS")
	}
	if seen.Header.Get("Origin") != nas.URL || seen.Header.Get("Referer") != nas.URL+"/storage" {
		t.Fatalf("origin: %q %q", seen.Header.Get("Origin"), seen.Header.Get("Referer"))
	}
	// Without the proxy cookie: refused.
	if r, _ := http.Get(root + "/"); r.StatusCode != 403 {
		t.Fatalf("no cookie: %d", r.StatusCode)
	}
}

func TestResolve(t *testing.T) {
	nas := newFakeNAS(t)
	local := t.TempDir()
	if err := os.MkdirAll(filepath.Join(local, "Sub dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "Sub dir", "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, _ := newTestAgent(t, nas, []engine.Pair{{ID: "p1", Local: local, Remote: "/srv/strata/v/Docs"}})
	got, err := a.resolve(filepath.Join(local, "Sub dir", "a.txt"))
	if err != nil || got.Remote != "/srv/strata/v/Docs/Sub dir/a.txt" || got.Account != MainAccount || got.Dir {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	if got, err := a.resolve(local); err != nil || got.Remote != "/srv/strata/v/Docs" || !got.Dir {
		t.Fatalf("resolve the folder: %+v %v", got, err)
	}
	if _, err := a.resolve(t.TempDir()); err == nil {
		t.Fatal("outside resolved")
	}
	if err := a.checkLocal(filepath.Join(a.Root, "accounts", "x")); err == nil {
		t.Fatal("folder of the agent accepted")
	}
}

func TestEvents(t *testing.T) {
	var l eventLog
	l.init()
	l.add(Event{Kind: "changed", Account: "a"})
	l.add(Event{Kind: "changed", Account: "a"})
	seq, evs := l.wait(context.Background(), 0, 0)
	if seq != 2 || len(evs) != 1 || evs[0].Seq != 2 {
		t.Fatalf("coalesced: %d %+v", seq, evs)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		l.add(Event{Kind: "notify", Title: "t"})
	}()
	seq, evs = l.wait(context.Background(), 2, 5*time.Second)
	if seq != 3 || len(evs) != 1 || evs[0].Title != "t" {
		t.Fatalf("wait: %d %+v", seq, evs)
	}
}

func TestStateHidesSecrets(t *testing.T) {
	nas := newFakeNAS(t)
	_, base := newTestAgent(t, nas, nil)
	r := do(t, "GET", base+"/v1/state", "", true)
	b, _ := io.ReadAll(r.Body)
	if strings.Contains(string(b), "stdev_x") {
		t.Fatal("device token in the state")
	}
	var v struct {
		Accounts []AccountView `json:"accounts"`
	}
	if err := json.Unmarshal(b, &v); err != nil || len(v.Accounts) != 1 || !v.Accounts[0].Paired {
		t.Fatalf("state: %s", b)
	}
}
