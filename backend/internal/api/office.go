package api

// OnlyOffice Document Server integration. The browser loads the editor from
// the document server with a configuration signed with the shared JWT secret.
// The document server downloads the file from a short-lived signed StrataOS
// URL and posts the edited version to a signed callback URL; StrataOS then
// fetches it from the document server only (never from the URL's host) and
// saves it like the text editor does.
//
// Addresses: the App Center container reaches StrataOS through
// host.docker.internal (extra_hosts: host-gateway) on the HTTP port 7070, and
// StrataOS reaches the container on 127.0.0.1:<app port>. Another server
// (or a test double) is set by hand in Settings → Office.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"strataos/internal/docker"
	"strataos/internal/jwt"
	"strataos/internal/store"
)

const (
	officeFileTTL     = time.Hour          // document download by the document server
	officeCallbackTTL = 7 * 24 * time.Hour // editing sessions can stay open for days
	officeMaxBytes    = 1 << 30            // largest document saved back
	defaultStrataURL  = "http://host.docker.internal:7070"
)

// officeTypes maps the extensions OnlyOffice opens to its document types.
var officeTypes = map[string]string{
	"docx": "word", "doc": "word", "odt": "word", "rtf": "word", "docm": "word", "dotx": "word", "ott": "word", "epub": "word",
	"xlsx": "cell", "xls": "cell", "ods": "cell", "csv": "cell", "xlsm": "cell", "xltx": "cell", "ots": "cell",
	"pptx": "slide", "ppt": "slide", "odp": "slide", "pptm": "slide", "potx": "slide", "otp": "slide",
	"pdf": "pdf",
}

// officeEditable: formats OnlyOffice saves back as they are (the others,
// legacy binary formats and PDF, open read-only).
var officeEditable = map[string]bool{"docx": true, "odt": true, "rtf": true, "xlsx": true, "ods": true, "csv": true, "pptx": true, "odp": true, "docm": true, "xlsm": true, "pptm": true}

type officeConf struct {
	enabled   bool
	stack     string // App Center stack ("" when not installed)
	port      string // app web port
	publicURL string // "" = the browser's NAS address with port
	internal  string // where StrataOS fetches saved documents
	strata    string // StrataOS as the document server reaches it
	secret    []byte
}

func (c officeConf) ready() bool {
	return c.enabled && len(c.secret) > 0 && c.internal != "" && (c.publicURL != "" || c.port != "")
}

func (s *Server) officeConf() officeConf {
	var o store.Office
	s.store.Read(func(st *store.State) { o = st.Office })
	c := officeConf{enabled: o.Enabled, publicURL: o.ServerURL, internal: o.InternalURL, strata: o.StrataURL, secret: []byte(o.Secret)}
	if c.stack = docker.AppStack(docker.OfficeApp); c.stack != "" {
		c.port = docker.EnvValue(c.stack, "WEB_PORT")
		if len(c.secret) == 0 {
			c.secret = []byte(docker.EnvValue(c.stack, "JWT_SECRET"))
		}
	}
	if c.internal == "" {
		if c.publicURL != "" && c.stack == "" {
			c.internal = c.publicURL
		} else if c.port != "" {
			c.internal = "http://127.0.0.1:" + c.port
		}
	}
	if c.strata == "" {
		c.strata = defaultStrataURL
	}
	return c
}

var officeKey []byte
var officeKeyMu sync.Mutex

// officeSigningKey signs StrataOS's own URLs (never shared with OnlyOffice).
func (s *Server) officeSigningKey() []byte {
	officeKeyMu.Lock()
	defer officeKeyMu.Unlock()
	if officeKey != nil {
		return officeKey
	}
	p := filepath.Join(s.dataDir, "office.key")
	if b, err := os.ReadFile(p); err == nil && len(b) == 32 {
		officeKey = b
		return b
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	_ = os.WriteFile(p, b, 0o600)
	officeKey = b
	return b
}

// officeClaims are carried by the signed file and callback URLs.
type officeClaims struct {
	Use  string `json:"use"` // file | cb
	Path string `json:"p"`
	User string `json:"u"`
	Key  string `json:"k,omitempty"` // document key (callback)
	Ver  string `json:"v,omitempty"` // file version opened (callback)
	Exp  int64  `json:"exp"`
}

func (s *Server) signOffice(c officeClaims, ttl time.Duration, now time.Time) (string, error) {
	c.Exp = now.Add(ttl).Unix()
	return jwt.Sign(s.officeSigningKey(), c)
}

func (s *Server) verifyOffice(tok, use string) (officeClaims, error) {
	var c officeClaims
	if err := jwt.Verify(s.officeSigningKey(), tok, &c); err != nil {
		return c, &Error{Status: http.StatusForbidden, Msg: "invalid or expired link"}
	}
	if c.Use != use || c.Path == "" || c.User == "" {
		return c, &Error{Status: http.StatusForbidden, Msg: "invalid or expired link"}
	}
	return c, nil
}

// officeDocKey is the same for everyone opening the same version of a file,
// which is what makes OnlyOffice join them in one co-editing session.
func (s *Server) officeDocKey(abs, etag string) string {
	m := hmac.New(sha256.New, s.officeSigningKey())
	m.Write([]byte(abs + "\x00" + etag))
	return hex.EncodeToString(m.Sum(nil))[:40]
}

// officeAccount returns whether the account behind a signed URL still exists,
// and its role (checked at use time, not when the URL was made).
func (s *Server) officeAccount(user string) (admin, ok bool) {
	s.store.Read(func(st *store.State) {
		if u := st.User(user); u != nil && !u.Disabled {
			admin, ok = u.Role == "admin", true
		}
	})
	return
}

// cleanBaseURL validates an http(s) base URL set by the administrator.
func cleanBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, " \"'<>\\") {
		return "", fmt.Errorf("invalid URL %s", raw)
	}
	return raw, nil
}

// officeFetchURL maps the download URL of a callback onto the configured
// document server: only its path and query are kept, so whatever host the
// callback names, StrataOS connects to the document server and nowhere else.
func officeFetchURL(internal, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "..") {
		return "", errors.New("invalid document URL")
	}
	base, err := url.Parse(internal)
	if err != nil || base.Host == "" {
		return "", errors.New("document server not configured")
	}
	out := *base
	out.Path = strings.TrimRight(base.Path, "/") + u.Path
	out.RawPath = ""
	out.RawQuery = u.RawQuery
	out.Fragment = ""
	return out.String(), nil
}

// callbackAction tells what to do with an OnlyOffice callback status.
// https://api.onlyoffice.com/docs/docs-api/usage-api/callback-handler/
func callbackAction(status int) string {
	switch status {
	case 2, 6: // ready for saving, force save
		return "save"
	case 3, 7: // saving failed on the document server
		return "error"
	case 1, 4: // being edited, closed without changes
		return "ignore"
	}
	return "unknown"
}

type officeCallback struct {
	Key      string   `json:"key"`
	Status   int      `json:"status"`
	URL      string   `json:"url"`
	FileType string   `json:"filetype"`
	Users    []string `json:"users"`
}

// parseCallback verifies the JWT of a callback (in the body, or the
// Authorization header) and returns the signed content, not the plain body.
func parseCallback(secret []byte, body []byte, authz string) (officeCallback, error) {
	var cb officeCallback
	var plain struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(body, &plain)
	tok := plain.Token
	if tok == "" {
		tok = strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
	}
	if tok == "" {
		return cb, errors.New("callback without token")
	}
	var claims map[string]json.RawMessage
	if err := jwt.Verify(secret, tok, &claims); err != nil {
		return cb, err
	}
	raw, _ := json.Marshal(claims)
	if p, ok := claims["payload"]; ok && plain.Token == "" { // header form wraps the body
		raw = p
	}
	if err := json.Unmarshal(raw, &cb); err != nil {
		return cb, errors.New("invalid callback")
	}
	return cb, nil
}

// officeSaved remembers the version each document key wrote last, so that a
// force save followed by the final save does not look like a conflict.
var officeSaved sync.Map // key → etag

var langRe = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)

func (s *Server) officeRoutes() {
	s.user("GET /api/office/status", func(r *http.Request) (any, error) {
		c := s.officeConf()
		return map[string]any{"enabled": c.ready(), "serverUrl": c.publicURL, "port": c.port, "types": officeTypes, "editable": officeEditable}, nil
	})

	s.admin("GET /api/office/settings", func(r *http.Request) (any, error) {
		var o store.Office
		s.store.Read(func(st *store.State) { o = st.Office })
		if o.Secret != "" {
			o.Secret = masked
		}
		c := s.officeConf()
		return map[string]any{"settings": o, "installed": c.stack != "", "stack": c.stack, "port": c.port, "ready": c.ready(),
			"defaultStrataUrl": defaultStrataURL, "internalUrl": c.internal, "appSecret": c.stack != "" && docker.EnvValue(c.stack, "JWT_SECRET") != ""}, nil
	})

	s.admin("PUT /api/office/settings", func(r *http.Request) (any, error) {
		var req store.Office
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var err error
		for _, p := range []*string{&req.ServerURL, &req.InternalURL, &req.StrataURL} {
			if *p, err = cleanBaseURL(*p); err != nil {
				return nil, badRequest(err.Error())
			}
		}
		req.Secret = strings.TrimSpace(req.Secret)
		if req.Secret != "" && req.Secret != masked && (len(req.Secret) < 16 || strings.ContainsAny(req.Secret, "\n\r")) {
			return nil, badRequest("the JWT secret needs at least 16 characters")
		}
		err = s.store.Update(func(st *store.State) error {
			if req.Secret == masked {
				req.Secret = st.Office.Secret
			}
			st.Office = req
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "system", "Office settings updated")
		return nil, nil
	})

	// Reachability of the document server from StrataOS.
	s.admin("POST /api/office/check", func(r *http.Request) (any, error) {
		c := s.officeConf()
		if c.internal == "" {
			return nil, badRequest("document server not configured")
		}
		cl := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := cl.Get(c.internal + "/healthcheck")
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		ok := resp.StatusCode == 200 && strings.TrimSpace(string(b)) == "true"
		out := map[string]any{"ok": ok}
		if !ok {
			out["error"] = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return out, nil
	})

	// Editor configuration for a file, signed for the document server.
	s.user("GET /api/office/config", func(r *http.Request) (any, error) {
		c := s.officeConf()
		if !c.ready() {
			return nil, badRequest("OnlyOffice is not enabled")
		}
		t, err := s.locate(r, r.URL.Query().Get("path"))
		if err != nil || !t.acc.read || t.acc.ancestor {
			return nil, firstErr(err, errForbidden)
		}
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(t.abs)), ".")
		docType, ok := officeTypes[ext]
		if !ok {
			return nil, badRequest("OnlyOffice cannot open this file type")
		}
		rt, err := t.open()
		if err != nil {
			return nil, err
		}
		fi, err := rt.Lstat(t.rel)
		rt.Close()
		if err != nil || !fi.Mode().IsRegular() {
			return nil, notFound("file not found")
		}
		me := userOf(r)
		etag := fileETag(fi)
		key := s.officeDocKey(t.abs, etag)
		edit := officeEditable[ext] && s.readOnlyReason(t) == "" && r.URL.Query().Get("mode") != "view"
		now := time.Now()
		fileTok, err := s.signOffice(officeClaims{Use: "file", Path: t.abs, User: me}, officeFileTTL, now)
		if err != nil {
			return nil, err
		}
		mode := "view"
		editorConfig := map[string]any{"user": map[string]string{"id": me, "name": me}}
		if edit {
			mode = "edit"
			cbTok, err := s.signOffice(officeClaims{Use: "cb", Path: t.abs, User: me, Key: key, Ver: etag}, officeCallbackTTL, now)
			if err != nil {
				return nil, err
			}
			editorConfig["callbackUrl"] = c.strata + "/api/office/callback?t=" + url.QueryEscape(cbTok)
		}
		editorConfig["mode"] = mode
		if l := r.URL.Query().Get("lang"); langRe.MatchString(l) {
			editorConfig["lang"] = l
		}
		cfg := map[string]any{
			"documentType": docType,
			"document": map[string]any{
				"fileType": ext, "key": key, "title": filepath.Base(t.abs),
				"url":         c.strata + "/api/office/file?t=" + url.QueryEscape(fileTok),
				"permissions": map[string]bool{"edit": edit, "download": true, "print": true, "comment": edit, "review": edit, "fillForms": edit},
			},
			"editorConfig": editorConfig,
		}
		tok, err := jwt.Sign(c.secret, cfg)
		if err != nil {
			return nil, err
		}
		cfg["token"] = tok
		return map[string]any{"serverUrl": c.publicURL, "port": c.port, "config": cfg}, nil
	})

	// The document server downloads the file (no session: the signed URL is the right).
	s.mux.HandleFunc("GET /api/office/file", func(w http.ResponseWriter, r *http.Request) {
		cl, err := s.verifyOffice(r.URL.Query().Get("t"), "file")
		if err != nil {
			writeErr(w, err)
			return
		}
		admin, ok := s.officeAccount(cl.User)
		if !ok {
			writeErr(w, errForbidden)
			return
		}
		t, err := s.locateFor(cl.User, admin, cl.Path)
		if err != nil || !t.acc.read || t.acc.ancestor {
			writeErr(w, firstErr(err, errForbidden))
			return
		}
		r.URL.RawQuery = "" // never inline
		s.serveTarget(w, r, t, nil)
	})

	s.mux.HandleFunc("POST /api/office/callback", func(w http.ResponseWriter, r *http.Request) {
		reply := func(code int) { writeJSON(w, http.StatusOK, map[string]int{"error": code}) }
		cl, err := s.verifyOffice(r.URL.Query().Get("t"), "cb")
		if err != nil {
			writeErr(w, err)
			return
		}
		c := s.officeConf()
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || len(c.secret) == 0 {
			writeErr(w, badRequest("invalid callback"))
			return
		}
		cb, err := parseCallback(c.secret, body, r.Header.Get("Authorization"))
		if err != nil {
			writeErr(w, &Error{Status: http.StatusForbidden, Msg: "invalid callback signature"})
			return
		}
		if cb.Key != cl.Key {
			writeErr(w, &Error{Status: http.StatusForbidden, Msg: "document key mismatch"})
			return
		}
		switch callbackAction(cb.Status) {
		case "ignore":
			reply(0)
			return
		case "error":
			s.store.Log("warning", "files", cl.User, "OnlyOffice could not save "+cl.Path)
			reply(0)
			return
		case "unknown":
			reply(1)
			return
		}
		if err := s.officeSave(c, cl, cb); err != nil {
			s.store.Log("error", "files", cl.User, "OnlyOffice save of "+cl.Path+" failed: "+err.Error())
			reply(1)
			return
		}
		reply(0)
	})
}

// officeSave fetches the edited document from the document server and writes
// it in place (or next to the file if it changed meanwhile, or when OnlyOffice
// returned another format).
func (s *Server) officeSave(c officeConf, cl officeClaims, cb officeCallback) error {
	src, err := officeFetchURL(c.internal, cb.URL)
	if err != nil {
		return err
	}
	admin, ok := s.officeAccount(cl.User)
	if !ok {
		return errors.New("account unavailable")
	}
	t, err := s.locateFor(cl.User, admin, cl.Path)
	if err != nil {
		return err
	}
	if !t.acc.write {
		return errors.New("no write access any more")
	}
	hc := &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
	resp, err := hc.Get(src)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("document server answered HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > officeMaxBytes {
		return errors.New("document too large")
	}
	body := &limitedReader{r: resp.Body, n: officeMaxBytes}

	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(t.abs)), ".")
	o := saveOpts{user: cl.User, admin: admin, force: true}
	note := ""
	dest := t
	if cb.FileType != "" && strings.ToLower(cb.FileType) != ext && regexp.MustCompile(`^[a-z0-9]{1,8}$`).MatchString(strings.ToLower(cb.FileType)) {
		dest, note = s.officeCopyTarget(t, strings.ToLower(cb.FileType), ""), ": another format"
		o = saveOpts{user: cl.User, admin: admin, create: true}
	} else if cur, err := s.currentETag(t); err == nil && cur != cl.Ver {
		if prev, ok := officeSaved.Load(cl.Key); !ok || prev.(string) != cur {
			// Changed by something else while being edited: keep both.
			dest, note = s.officeCopyTarget(t, ext, time.Now().Format("2006-01-02 15.04")), ": the file changed meanwhile"
			o = saveOpts{user: cl.User, admin: admin, create: true}
		}
	}
	res, err := s.saveFile(dest, body, o)
	if err != nil {
		return err
	}
	if body.over {
		return errors.New("document too large")
	}
	if dest.abs == t.abs {
		officeSaved.Store(cl.Key, res.ETag)
		s.store.Log("info", "files", cl.User, "Saved file "+dest.abs+" (OnlyOffice)")
	} else {
		s.store.Log("warning", "files", cl.User, "OnlyOffice saved "+t.abs+" as "+dest.abs+note)
	}
	return nil
}

// limitedReader fails once more than n bytes were read.
type limitedReader struct {
	r    io.Reader
	n    int64
	over bool
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		l.over = true
		return 0, errors.New("document too large")
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

func (s *Server) currentETag(t target) (string, error) {
	rt, err := t.open()
	if err != nil {
		return "", err
	}
	defer rt.Close()
	fi, err := rt.Lstat(t.rel)
	if err != nil {
		return "", err
	}
	return fileETag(fi), nil
}

// officeCopyTarget returns a free name next to t: "<name> (<tag>).<ext>".
func (s *Server) officeCopyTarget(t target, ext, tag string) target {
	base := strings.TrimSuffix(path.Base(t.rel), path.Ext(t.rel))
	dir := path.Dir(t.rel)
	rt, err := t.open()
	if err == nil {
		defer rt.Close()
	}
	for i := 1; i < 1000; i++ {
		name := base + "." + ext
		switch {
		case tag != "" && i == 1:
			name = base + " (" + tag + ")." + ext
		case tag != "":
			name = fmt.Sprintf("%s (%s %d).%s", base, tag, i, ext)
		case i > 1:
			name = fmt.Sprintf("%s (%d).%s", base, i, ext)
		}
		c := t
		c.rel = path.Join(dir, name)
		c.abs = filepath.Join(filepath.Dir(t.abs), name)
		if rt == nil {
			return c
		}
		if _, err := rt.Lstat(c.rel); errors.Is(err, os.ErrNotExist) {
			return c
		}
	}
	return t
}
