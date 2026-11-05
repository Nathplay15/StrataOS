// Package agent is the sync engine run by the desktop app (desktop/app, a
// Tauri shell) as a sidecar process: it holds the accounts (one per NAS),
// runs their engines and answers the app's window over a local HTTP API.
//
// The API listens on 127.0.0.1 only and every call carries a random token
// that the shell creates at each launch and passes in the environment
// (STRATAOS_AGENT_TOKEN), so other local programs and web pages cannot use
// it. The window also reaches the NAS through it (proxy.go): the agent adds
// the device token and checks the pinned certificate, so the token never
// leaves this process and the webview never meets a self-signed certificate.
package agent

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"strataos-sync/internal/api"
	"strataos-sync/internal/app"
	"strataos-sync/internal/config"
	"strataos-sync/internal/i18n"
)

// MainAccount is the account kept in the root folder itself, where the
// headless commands and the former tray app keep theirs.
const MainAccount = "main"

var accountID = regexp.MustCompile(`^[a-z0-9]{1,24}$`)

// Agent is the running sidecar.
type Agent struct {
	Root    string // folder of the settings (config.DefaultDir())
	Version string
	Token   string // per-launch API token
	// Dev allows the Vite development server as an origin.
	Dev bool

	ctx      context.Context
	mu       sync.Mutex
	accounts map[string]*account
	events   eventLog
	xfers    transfers
	enter    map[string]enterKey // one-time keys of the web interface windows
	lang     string
}

type account struct {
	id  string
	app *app.App
	web *webProxy
}

// New loads the accounts of root.
func New(ctx context.Context, root, version, token string) (*Agent, error) {
	if len(token) < 32 {
		return nil, errors.New("agent token too short")
	}
	a := &Agent{Root: root, Version: version, Token: token, ctx: ctx, accounts: map[string]*account{}, enter: map[string]enterKey{}}
	a.events.init()
	a.xfers.init(a)
	if err := os.MkdirAll(filepath.Join(root, "accounts"), 0o700); err != nil {
		return nil, err
	}
	if err := a.load(MainAccount, root); err != nil {
		return nil, err
	}
	ents, _ := os.ReadDir(filepath.Join(root, "accounts"))
	for _, e := range ents {
		if e.IsDir() && accountID.MatchString(e.Name()) && e.Name() != MainAccount {
			if err := a.load(e.Name(), filepath.Join(root, "accounts", e.Name())); err != nil {
				log.Printf("account %s: %v", e.Name(), err)
			}
		}
	}
	if c := a.accounts[MainAccount].app.Config(); c.Language != "" {
		a.lang = c.Language
	}
	return a, nil
}

func (a *Agent) load(id, dir string) error {
	x, err := app.New(&config.Store{Dir: dir}, a.Version)
	if err != nil {
		return err
	}
	acc := &account{id: id, app: x}
	x.Notify = func(title, body string) { a.events.add(Event{Kind: "notify", Account: id, Title: title, Body: body}) }
	x.OnChange(func() { a.events.add(Event{Kind: "changed", Account: id}) })
	if err := x.Start(a.ctx); err != nil {
		log.Printf("account %s: start: %v", id, err)
	}
	a.mu.Lock()
	a.accounts[id] = acc
	a.mu.Unlock()
	return nil
}

// Stop stops every engine.
func (a *Agent) Stop() {
	a.mu.Lock()
	l := make([]*account, 0, len(a.accounts))
	for _, x := range a.accounts {
		l = append(l, x)
	}
	a.mu.Unlock()
	for _, x := range l {
		x.app.Stop()
	}
}

func (a *Agent) account(id string) (*account, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if x := a.accounts[id]; x != nil {
		return x, nil
	}
	return nil, &httpError{http.StatusNotFound, "unknown account"}
}

// sorted lists the accounts, the main one first.
func (a *Agent) sorted() []*account {
	a.mu.Lock()
	defer a.mu.Unlock()
	l := make([]*account, 0, len(a.accounts))
	for _, x := range a.accounts {
		l = append(l, x)
	}
	sort.Slice(l, func(i, j int) bool {
		if (l[i].id == MainAccount) != (l[j].id == MainAccount) {
			return l[i].id == MainAccount
		}
		return l[i].id < l[j].id
	})
	return l
}

// T translates a text in the language of the app.
func (a *Agent) T(id string, args ...any) string {
	a.mu.Lock()
	l := a.lang
	a.mu.Unlock()
	return i18n.T(i18n.Pick(l), id, args...)
}

// ---- Server ----

// Listen starts the API on a free port of 127.0.0.1 and returns it.
func (a *Agent) Listen() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-a.ctx.Done()
		_ = srv.Close()
	}()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// allowedOrigin: the origins of the app's own window (tauri://localhost on
// macOS and Linux, http(s)://tauri.localhost on Windows).
func (a *Agent) allowedOrigin(o string) bool {
	switch o {
	case "tauri://localhost", "http://tauri.localhost", "https://tauri.localhost":
		return true
	case "http://localhost:1420", "http://127.0.0.1:1420":
		return a.Dev
	}
	return false
}

// authed checks the host name (no DNS rebinding) and the token: in the
// Authorization header, or for GET requests of pictures, videos and
// downloads that the webview makes by itself, in the "st" parameter.
func (a *Agent) authed(r *http.Request) bool {
	host, _, _ := net.SplitHostPort(r.Host)
	if host != "127.0.0.1" {
		return false
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		tok = r.URL.Query().Get("st")
	}
	return tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(a.Token)) == 1
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

// Handler serves the API.
func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	a.routes(func(pattern string, h func(r *http.Request) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			v, err := h(r)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			if err != nil {
				status := http.StatusBadRequest
				var he *httpError
				if errors.As(err, &he) {
					status = he.status
				} else if s := api.StatusOf(err); s >= 400 {
					status = s
				}
				out := map[string]any{"error": a.message(err)}
				if errors.Is(err, api.ErrTwoFactor) {
					out["twoFactor"] = true
				}
				var ce *api.CertError
				if errors.As(err, &ce) {
					out["certificate"] = ce.Presented
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(out)
				return
			}
			if v == nil {
				v = map[string]bool{"ok": true}
			}
			_ = json.NewEncoder(w).Encode(v)
		})
	})
	mux.HandleFunc("/v1/accounts/{id}/nas/{rest...}", a.serveNAS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" {
			if !a.allowedOrigin(o) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", o)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Expose-Headers", "Content-Disposition, Content-Length, Content-Range, X-Strata-Version")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Range, If-Range, Upload-Offset")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if !a.authed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// message explains an error in the user's language when it is a known one.
func (a *Agent) message(err error) string {
	var ce *api.CertError
	switch {
	case errors.As(err, &ce) && ce.Pinned != "":
		return a.T("err.certchanged", ce.Presented)
	case errors.As(err, &ce):
		return a.T("err.certuntrusted", ce.Presented)
	case errors.Is(err, api.ErrTwoFactor):
		return a.T("err.twofactor")
	case api.StatusOf(err) == http.StatusUnauthorized:
		return a.T("err.unauthorized") + " (" + err.Error() + ")"
	}
	return err.Error()
}

func decodeBody(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return errors.New("invalid request")
	}
	return nil
}

func newKey(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newAccountDir creates the folder of a new account.
func (a *Agent) newAccountDir() (string, string, error) {
	for range 10 {
		id := newKey(4)
		dir := filepath.Join(a.Root, "accounts", id)
		if err := os.Mkdir(dir, 0o700); err == nil {
			return id, dir, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("cannot create an account folder")
}
