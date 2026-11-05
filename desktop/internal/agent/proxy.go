package agent

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos-sync/internal/api"
)

// ---- NAS API, for the window ----

// requestHeaders are the headers of the window passed on to the NAS.
var requestHeaders = []string{"Accept", "Content-Type", "Range", "If-Range", "If-None-Match", "Upload-Offset", "Upload-Length"}

// serveNAS forwards /v1/accounts/{id}/nas/<path> to the NAS with the
// device token. The window's own headers (its Origin, the agent token)
// are not passed on: the NAS sees an API client like the Android app.
func (a *Agent) serveNAS(w http.ResponseWriter, r *http.Request) {
	x, err := a.account(r.PathValue("id"))
	var c *api.Client
	if err == nil {
		c = x.app.Client()
	}
	if c == nil {
		writeJSONError(w, http.StatusNotFound, a.T("err.notpaired"))
		return
	}
	rest := r.PathValue("rest")
	if !strings.HasPrefix(rest, "api/") || strings.Contains(rest, "..") {
		writeJSONError(w, http.StatusForbidden, "only the API of the NAS")
		return
	}
	q := r.URL.Query()
	q.Del("st")
	out, err := c.Request(r.Context(), r.Method, "/"+rest, q, r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	out.ContentLength = r.ContentLength
	for _, h := range requestHeaders {
		if v := r.Header.Get(h); v != "" {
			out.Header.Set(h, v)
		}
	}
	out.Header.Set("X-Strata", "1")
	resp, err := c.Transport().RoundTrip(out)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, a.message(err))
		return
	}
	defer resp.Body.Close()
	h := w.Header()
	for k, v := range resp.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Set-Cookie", "Strict-Transport-Security", "Connection", "Keep-Alive", "Transfer-Encoding", "Alt-Svc":
			continue
		}
		h[k] = v
	}
	if resp.StatusCode == http.StatusUnauthorized {
		a.events.add(Event{Kind: "changed", Account: x.id})
	}
	w.WriteHeader(resp.StatusCode)
	rc := http.NewResponseController(w)
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush() // long polls and progressive answers
		}
		if rerr != nil {
			return
		}
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := jsonMarshal(map[string]string{"error": msg})
	_, _ = w.Write(b)
}

// ---- Web interface, in a window of the app ----

// The complete web interface opens in a window of the app (deep management
// pages, VM consoles…), through a reverse proxy of its own on another port
// of 127.0.0.1 (the interface uses absolute paths). The agent signs in with
// the device token (POST /api/mobile/websession, like the Android app) and
// keeps the session cookie itself: the webview only holds a cookie for the
// proxy, given once through a one-time link. So the pinned certificate is
// checked by the agent and no other local program can use the session.

type enterKey struct {
	account string
	path    string
	expires time.Time
}

type webProxy struct {
	a       *Agent
	acc     *account
	port    int
	gate    string // value of the proxy cookie
	srv     *http.Server
	mu      sync.Mutex
	cookies map[string]*http.Cookie // the NAS session, kept here
}

func (w *webProxy) cookieName() string { return "strataos_web_" + strconv.Itoa(w.port) }

func (w *webProxy) close() { _ = w.srv.Close() }

// openWeb returns a one-time address opening the web interface at path.
func (a *Agent) openWeb(x *account, p string) (string, error) {
	if !x.app.Paired() {
		return "", errors.New(a.T("err.notpaired"))
	}
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
		p = "/"
	}
	a.mu.Lock()
	wp := x.web
	a.mu.Unlock()
	if wp == nil {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
		wp = &webProxy{a: a, acc: x, port: ln.Addr().(*net.TCPAddr).Port, gate: newKey(24), cookies: map[string]*http.Cookie{}}
		wp.srv = &http.Server{Handler: wp, ReadHeaderTimeout: 10 * time.Second}
		go func() { _ = wp.srv.Serve(ln) }()
		go func() {
			<-a.ctx.Done()
			wp.close()
		}()
		a.mu.Lock()
		if x.web != nil { // opened meanwhile
			a.mu.Unlock()
			wp.close()
			wp = x.web
		} else {
			x.web = wp
			a.mu.Unlock()
		}
	}
	k := newKey(24)
	a.mu.Lock()
	for key, e := range a.enter {
		if time.Now().After(e.expires) {
			delete(a.enter, key)
		}
	}
	a.enter[k] = enterKey{account: x.id, path: p, expires: time.Now().Add(time.Minute)}
	a.mu.Unlock()
	return "http://127.0.0.1:" + strconv.Itoa(wp.port) + "/__strataos/enter?k=" + k, nil
}

func (w *webProxy) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.Host)
	if host != "127.0.0.1" {
		http.Error(rw, "forbidden", http.StatusForbidden)
		return
	}
	if r.URL.Path == "/__strataos/enter" {
		w.enter(rw, r)
		return
	}
	ck, err := r.Cookie(w.cookieName())
	if err != nil || subtle.ConstantTimeCompare([]byte(ck.Value), []byte(w.gate)) != 1 {
		http.Error(rw, w.a.T("web.reopen"), http.StatusForbidden)
		return
	}
	c := w.acc.app.Client()
	if c == nil {
		http.Error(rw, w.a.T("err.notpaired"), http.StatusForbidden)
		return
	}
	base, err := url.Parse(c.Base)
	if err != nil {
		http.Error(rw, "invalid NAS address", http.StatusBadGateway)
		return
	}
	rp := &httputil.ReverseProxy{
		Transport:     c.Transport(),
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(base)
			pr.Out.Host = base.Host
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			for _, ck := range w.jar() {
				pr.Out.AddCookie(ck)
			}
			// The NAS compares Origin and Referer with its own address (CSRF guard).
			if pr.In.Header.Get("Origin") != "" {
				pr.Out.Header.Set("Origin", c.Base)
			}
			if ref := pr.In.Header.Get("Referer"); ref != "" {
				if u, err := url.Parse(ref); err == nil {
					pr.Out.Header.Set("Referer", c.Base+u.RequestURI())
				} else {
					pr.Out.Header.Del("Referer")
				}
			}
			pr.Out.Header.Del("X-Forwarded-For")
		},
		ModifyResponse: func(resp *http.Response) error {
			w.keep(resp.Cookies())
			resp.Header.Del("Set-Cookie")
			resp.Header.Del("Strict-Transport-Security")
			if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, c.Base+"/") {
				resp.Header.Set("Location", strings.TrimPrefix(loc, c.Base))
			}
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, r *http.Request, err error) {
			http.Error(rw, w.a.message(err), http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(rw, r)
}

// enter takes a one-time key, signs in to the NAS and gives the webview
// the proxy cookie.
func (w *webProxy) enter(rw http.ResponseWriter, r *http.Request) {
	k := r.URL.Query().Get("k")
	w.a.mu.Lock()
	e, ok := w.a.enter[k]
	delete(w.a.enter, k)
	w.a.mu.Unlock()
	if !ok || time.Now().After(e.expires) || e.account != w.acc.id {
		http.Error(rw, w.a.T("web.reopen"), http.StatusForbidden)
		return
	}
	if err := w.signIn(r.Context()); err != nil {
		http.Error(rw, w.a.message(err), http.StatusBadGateway)
		return
	}
	http.SetCookie(rw, &http.Cookie{Name: w.cookieName(), Value: w.gate, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(rw, r, e.path, http.StatusFound)
}

// signIn trades the device token for a web session (kept in the jar).
func (w *webProxy) signIn(ctx context.Context) error {
	c := w.acc.app.Client()
	if c == nil {
		return errors.New(w.a.T("err.notpaired"))
	}
	var v struct {
		Path string `json:"path"`
	}
	if err := c.Call(ctx, "POST", "/api/mobile/websession", nil, map[string]any{}, &v); err != nil {
		return err
	}
	if !strings.HasPrefix(v.Path, "/api/mobile/websession/") {
		return errors.New("unexpected answer from the NAS")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+v.Path, nil)
	if err != nil {
		return err
	}
	resp, err := c.Transport().RoundTrip(req) // no redirects followed: the cookie is on this answer
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return api.ReadError(resp)
	}
	w.mu.Lock()
	w.cookies = map[string]*http.Cookie{}
	w.mu.Unlock()
	w.keep(resp.Cookies())
	return nil
}

// keep stores (or removes) the cookies set by the NAS.
func (w *webProxy) keep(l []*http.Cookie) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, ck := range l {
		if ck.MaxAge < 0 || (!ck.Expires.IsZero() && ck.Expires.Before(time.Now())) || ck.Value == "" {
			delete(w.cookies, ck.Name)
			continue
		}
		w.cookies[ck.Name] = &http.Cookie{Name: ck.Name, Value: ck.Value}
	}
}

func (w *webProxy) jar() []*http.Cookie {
	w.mu.Lock()
	defer w.mu.Unlock()
	l := make([]*http.Cookie, 0, len(w.cookies))
	for _, ck := range w.cookies {
		l = append(l, ck)
	}
	return l
}
