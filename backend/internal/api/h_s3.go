package api

// Built-in S3 service (internal/s3gw): shared folders exposed as buckets,
// per-user access keys whose rights follow the share permissions (read or
// read-write, read-only over quota or frozen), HTTPS with the NAS
// certificate. Unlike the MinIO app, it keeps no data of its own: the
// objects are the folder's files.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/accounts"
	"strataos/internal/s3gw"
	"strataos/internal/store"
	"strataos/internal/sys"
)

const maxS3Keys = 10

var s3Mu sync.Mutex

func s3Installed() bool {
	_, err := os.Stat(s3gw.Binary)
	return err == nil
}

func newS3Key() (string, string) {
	a := make([]byte, 10)
	sec := make([]byte, 30)
	_, _ = rand.Read(a)
	_, _ = rand.Read(sec)
	return "STRA" + base32.StdEncoding.EncodeToString(a), base64.RawURLEncoding.EncodeToString(sec)
}

func s3Port(c store.S3Service) int {
	if c.Port > 0 {
		return c.Port
	}
	return s3gw.DefaultPort
}

// s3Grants computes the bucket ACLs: every key of a user who may read the
// shared folder, writable where the user may write.
func s3Grants(st *store.State, groups func(string) []string) map[string][]s3gw.Grant {
	out := map[string][]s3gw.Grant{}
	for _, b := range st.S3.Buckets {
		sh := st.Share(b.Share)
		out[b.Name] = []s3gw.Grant{}
		if sh == nil {
			continue
		}
		for _, u := range st.Users {
			if u.Disabled || len(u.S3Keys) == 0 {
				continue
			}
			perm := sharePerm(sh, u.Username, groups(u.Username))
			read, _ := s3gw.Perm(perm)
			write := sh.Writable(perm) // read-only over quota or frozen
			if !read {
				continue
			}
			for _, k := range u.S3Keys {
				out[b.Name] = append(out[b.Name], s3gw.Grant{Access: k.Access, Write: write})
			}
		}
	}
	return out
}

// applyS3 writes the accounts, buckets and ACLs of the gateway and
// (re)starts or stops it.
func (s *Server) applyS3() error {
	s3Mu.Lock()
	defer s3Mu.Unlock()
	var c store.S3Service
	s.store.Read(func(st *store.State) { c = st.S3 })
	if !c.Enabled {
		if _, err := os.Stat(s3gw.UnitPath); err == nil {
			_, _ = sys.Run("systemctl", "disable", "--now", s3gw.Unit)
			_ = os.Remove(s3gw.UnitPath)
			_, _ = sys.Run("systemctl", "daemon-reload")
		}
		return nil
	}
	if !s3Installed() {
		return errors.New("install the S3 service first (Settings → Components)")
	}
	if c.RootAccess == "" || c.RootSecret == "" {
		a, sec := newS3Key()
		if err := s.store.Update(func(st *store.State) error {
			st.S3.RootAccess, st.S3.RootSecret = a, sec
			return nil
		}); err != nil {
			return err
		}
		c.RootAccess, c.RootSecret = a, sec
	}
	for _, d := range []string{s3gw.Dir, s3gw.BucketsDir, s3gw.MetaDir, s3gw.IAMDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	_ = os.MkdirAll(filepath.Dir(s3gw.AccessLog), 0o750)

	// Accounts (one per key) and grants, from a snapshot of the state.
	type keyOf struct {
		user string
		key  store.S3Key
	}
	var keys []keyOf
	links := map[string]string{}
	var grants map[string][]s3gw.Grant
	s.store.Read(func(st *store.State) {
		for _, u := range st.Users {
			if u.Disabled {
				continue
			}
			for _, k := range u.S3Keys {
				keys = append(keys, keyOf{u.Username, k})
			}
		}
		for _, b := range st.S3.Buckets {
			if sh := st.Share(b.Share); sh != nil && !sh.Encrypted {
				links[b.Name] = sh.Path
			}
		}
		grants = s3Grants(st, accounts.UserGroups)
	})
	var accts []s3gw.Account
	for _, k := range keys {
		uid, gid, err := accounts.IDs(k.user)
		if err != nil {
			slog.Warn("s3: no system account", "user", k.user)
			continue
		}
		accts = append(accts, s3gw.Account{Access: k.key.Access, Secret: k.key.Secret, UID: uid, GID: gid})
	}
	if err := sys.WriteFileAtomic(filepath.Join(s3gw.IAMDir, "users.json"), s3gw.IAMJSON(accts), 0o600); err != nil {
		return err
	}

	// Buckets: links to the shared folders, nothing else in the folder.
	entries, _ := os.ReadDir(s3gw.BucketsDir)
	for _, e := range entries {
		p := filepath.Join(s3gw.BucketsDir, e.Name())
		if target, err := os.Readlink(p); err != nil || links[e.Name()] != target {
			_ = os.Remove(p)
		}
	}
	for name, target := range links {
		p := filepath.Join(s3gw.BucketsDir, name)
		if t, err := os.Readlink(p); err == nil && t == target {
			continue
		}
		if err := os.Symlink(target, p); err != nil {
			return err
		}
	}
	for name, g := range grants {
		dir := filepath.Join(s3gw.MetaDir, name, "meta")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := sys.WriteFileAtomic(filepath.Join(dir, "acl"), s3gw.ACLJSON(c.RootAccess, g), 0o600); err != nil {
			return err
		}
	}

	// The service itself: restarted when its unit or root credentials change.
	o := s3gw.Options{Port: s3Port(c), Domain: c.Domain}
	if s.certs != nil {
		cert, key := s.certs.Files()
		if data, err := os.ReadFile(cert); err == nil {
			o.CertFile, o.KeyFile = cert, key
			h := sha256.Sum256(data)
			o.CertID = hex.EncodeToString(h[:6])
		}
	}
	changed := false
	for path, content := range map[string]string{s3gw.UnitPath: s3gw.UnitFile(o), s3gw.EnvFile: s3gw.EnvFileContent(c.RootAccess, c.RootSecret)} {
		if old, err := os.ReadFile(path); err == nil && string(old) == content {
			continue
		}
		mode := os.FileMode(0o644)
		if path == s3gw.EnvFile {
			mode = 0o600
		}
		if err := sys.WriteFileAtomic(path, []byte(content), mode); err != nil {
			return err
		}
		changed = true
	}
	_ = sys.WriteFileAtomic("/etc/logrotate.d/strata-s3", []byte(s3gw.AccessLog+" {\n  weekly\n  rotate 8\n  compress\n  missingok\n  notifempty\n  copytruncate\n}\n"), 0o644)
	if changed {
		if _, err := sys.Run("systemctl", "daemon-reload"); err != nil {
			return err
		}
		if _, err := sys.Run("systemctl", "enable", s3gw.Unit); err != nil {
			return err
		}
		_, err := sys.Run("systemctl", "restart", s3gw.Unit)
		return err
	}
	if _, err := sys.Run("systemctl", "is-active", "--quiet", s3gw.Unit); err != nil {
		_, err = sys.Run("systemctl", "start", s3gw.Unit)
		return err
	}
	return nil
}

// s3Changed re-applies the service after a change of shares, users or
// groups (applyShares), when it is on.
func (s *Server) s3Changed() {
	var on bool
	s.store.Read(func(st *store.State) { on = st.S3.Enabled })
	if on {
		go s.applyS3Logged()
	}
}

func (s *Server) applyS3Logged() {
	if err := s.applyS3(); err != nil {
		slog.Warn("s3", "err", err)
		s.store.Log("error", "s3", "", "S3 service: "+err.Error())
	}
}

// s3Loop applies the configuration at start and every 6 hours (a renewed
// certificate restarts the service).
func (s *Server) s3Loop() {
	for {
		var on bool
		s.store.Read(func(st *store.State) { on = st.S3.Enabled })
		if on {
			s.applyS3Logged()
		}
		time.Sleep(6 * time.Hour)
	}
}

// s3Endpoint is the address clients use, on the name the page is open with.
func (s *Server) s3Endpoint(r *http.Request, c store.S3Service) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	scheme := "https"
	if s.certs == nil {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, s3Port(c))
}

type s3BucketView struct {
	Name   string `json:"name"`
	Share  string `json:"share"`
	Path   string `json:"path,omitempty"`
	Perm   string `json:"perm,omitempty"` // for the user: rw | ro
	Issue  string `json:"issue,omitempty"`
	Quota  bool   `json:"readOnly,omitempty"`
	Access int    `json:"keys"` // keys with access (admin view)
}

type s3KeyView struct {
	Access  string    `json:"access"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
}

func (s *Server) s3Routes() {
	go s.s3Loop()

	s.admin("GET /api/s3", func(r *http.Request) (any, error) {
		var c store.S3Service
		var buckets []s3BucketView
		var shares []string
		s.store.Read(func(st *store.State) {
			c = st.S3
			grants := s3Grants(st, accounts.UserGroups)
			for _, b := range c.Buckets {
				v := s3BucketView{Name: b.Name, Share: b.Share, Access: len(grants[b.Name])}
				if sh := st.Share(b.Share); sh == nil {
					v.Issue = "shared folder deleted"
				} else if sh.Encrypted {
					v.Issue = "encrypted shared folders cannot be exposed"
				} else {
					v.Path, v.Quota = sh.Path, sh.ReadOnly()
				}
				buckets = append(buckets, v)
			}
			for _, sh := range st.Shares {
				shares = append(shares, sh.Name)
			}
		})
		if buckets == nil {
			buckets = []s3BucketView{}
		}
		if shares == nil {
			shares = []string{}
		}
		_, activeErr := sys.Run("systemctl", "is-active", "--quiet", s3gw.Unit)
		return map[string]any{"installed": s3Installed(), "enabled": c.Enabled, "running": c.Enabled && activeErr == nil, "port": s3Port(c),
			"domain": c.Domain, "buckets": buckets, "shares": shares, "endpoint": s.s3Endpoint(r, c)}, nil
	})

	s.admin("PUT /api/s3", func(r *http.Request) (any, error) {
		var req struct {
			Enabled bool             `json:"enabled"`
			Port    int              `json:"port"`
			Domain  string           `json:"domain"`
			Buckets []store.S3Bucket `json:"buckets"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
		if req.Port == 0 {
			req.Port = s3gw.DefaultPort
		}
		if req.Port < 1024 || req.Port > 65535 || slices.Contains([]int{7070, 7443}, req.Port) {
			return nil, badRequest("choose a port between 1024 and 65535, other than those of the web interface")
		}
		if err := s3gw.ValidDomain(req.Domain); err != nil {
			return nil, badRequest(err.Error())
		}
		if len(req.Buckets) > 200 {
			return nil, badRequest("200 buckets at most")
		}
		if req.Enabled && !s3Installed() {
			return nil, badRequest("install the S3 service first (Settings → Components)")
		}
		var old store.S3Service
		s.store.Read(func(st *store.State) { old = st.S3 })
		if req.Enabled && (!old.Enabled || s3Port(old) != req.Port) {
			// The port must be free (the MinIO app uses 9000 and 9001).
			l, err := net.Listen("tcp", ":"+strconv.Itoa(req.Port))
			if err != nil {
				return nil, badRequest(fmt.Sprintf("port %d is already used on the NAS", req.Port))
			}
			l.Close()
		}
		seenName, seenShare := map[string]bool{}, map[string]bool{}
		err := s.store.Update(func(st *store.State) error {
			for i, b := range req.Buckets {
				b.Name = strings.TrimSpace(b.Name)
				if b.Name == "" {
					b.Name = s3gw.BucketName(b.Share)
				}
				if err := s3gw.ValidBucketName(b.Name); err != nil {
					return badRequest(err.Error())
				}
				sh := st.Share(b.Share)
				switch {
				case sh == nil:
					return badRequest("unknown shared folder " + b.Share)
				case sh.Encrypted:
					return badRequest("encrypted shared folders cannot be exposed over S3: " + b.Share)
				case seenName[b.Name]:
					return badRequest("bucket name used twice: " + b.Name)
				case seenShare[b.Share]:
					return badRequest("shared folder exposed twice: " + b.Share)
				}
				seenName[b.Name], seenShare[b.Share] = true, true
				req.Buckets[i] = b
			}
			st.S3.Enabled, st.S3.Port, st.S3.Domain, st.S3.Buckets = req.Enabled, req.Port, req.Domain, req.Buckets
			return nil
		})
		if err != nil {
			return nil, err
		}
		names := make([]string, len(req.Buckets))
		for i, b := range req.Buckets {
			names[i] = b.Name + "=" + b.Share
		}
		if req.Enabled {
			s.audit(r, "s3", fmt.Sprintf("S3 service on port %d, buckets: %s", req.Port, strings.Join(names, ", ")))
		} else if old.Enabled {
			s.audit(r, "s3", "S3 service turned off")
		}
		if err := s.applyS3(); err != nil {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		return nil, nil
	})

	// The last requests (access log), with the user of each key.
	s.admin("GET /api/s3/log", func(r *http.Request) (any, error) {
		n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
		if n <= 0 || n > 1000 {
			n = 200
		}
		type entry struct {
			s3gw.Entry
			User string `json:"user,omitempty"`
		}
		out := []entry{}
		f, err := os.Open(s3gw.AccessLog)
		if err != nil {
			return out, nil
		}
		defer f.Close()
		// Only the end of a large log is read.
		if fi, err := f.Stat(); err == nil && fi.Size() > 4<<20 {
			_, _ = f.Seek(fi.Size()-4<<20, 0)
		}
		owners := map[string]string{}
		s.store.Read(func(st *store.State) {
			for _, u := range st.Users {
				for _, k := range u.S3Keys {
					owners[k.Access] = u.Username
				}
			}
		})
		for _, e := range s3gw.Tail(f, n) {
			out = append(out, entry{e, owners[e.Access]})
		}
		return out, nil
	})

	s.user("GET /api/me/s3", func(r *http.Request) (any, error) {
		me := userOf(r)
		groups := accounts.UserGroups(me)
		var c store.S3Service
		keys := []s3KeyView{}
		buckets := []s3BucketView{}
		s.store.Read(func(st *store.State) {
			c = st.S3
			if u := st.User(me); u != nil {
				for _, k := range u.S3Keys {
					keys = append(keys, s3KeyView{k.Access, k.Name, k.Created})
				}
			}
			for _, b := range c.Buckets {
				sh := st.Share(b.Share)
				if sh == nil || sh.Encrypted {
					continue
				}
				perm := sharePerm(sh, me, groups)
				if read, _ := s3gw.Perm(perm); read {
					if !sh.Writable(perm) {
						perm = "ro"
					}
					buckets = append(buckets, s3BucketView{Name: b.Name, Share: b.Share, Perm: perm, Quota: sh.ReadOnly()})
				}
			}
		})
		return map[string]any{"enabled": c.Enabled, "endpoint": s.s3Endpoint(r, c), "domain": c.Domain, "region": "us-east-1", "keys": keys, "buckets": buckets}, nil
	})

	// A new key: the secret is in this answer only.
	s.user("POST /api/me/s3/keys", func(r *http.Request) (any, error) {
		var req struct {
			Name string `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name := oidcText(req.Name, 64)
		if name == "" {
			name = "S3 key"
		}
		access, secret := newS3Key()
		err := s.store.Update(func(st *store.State) error {
			u := st.User(userOf(r))
			if u == nil {
				return notFound("user not found")
			}
			if len(u.S3Keys) >= maxS3Keys {
				return badRequest(fmt.Sprintf("%d keys at most: revoke one first", maxS3Keys))
			}
			u.S3Keys = append(u.S3Keys, store.S3Key{Access: access, Secret: secret, Name: name, Created: time.Now()})
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "s3", fmt.Sprintf("S3 access key %s created (%s)", access, name))
		go s.applyS3Logged()
		return map[string]any{"access": access, "secret": secret, "name": name}, nil
	})

	s.user("DELETE /api/me/s3/keys/{access}", func(r *http.Request) (any, error) {
		return nil, s.revokeS3Key(r, userOf(r), r.PathValue("access"))
	})

	s.admin("DELETE /api/users/{name}/s3-keys/{access}", func(r *http.Request) (any, error) {
		return nil, s.revokeS3Key(r, r.PathValue("name"), r.PathValue("access"))
	})
}

func (s *Server) revokeS3Key(r *http.Request, user, access string) error {
	err := s.store.Update(func(st *store.State) error {
		u := st.User(user)
		if u == nil || !slices.ContainsFunc(u.S3Keys, func(k store.S3Key) bool { return k.Access == access }) {
			return notFound("key not found")
		}
		u.S3Keys = slices.DeleteFunc(u.S3Keys, func(k store.S3Key) bool { return k.Access == access })
		return nil
	})
	if err != nil {
		return err
	}
	s.audit(r, "s3", fmt.Sprintf("S3 access key %s of %s revoked", access, user))
	// At once: a revoked key must stop working now.
	if err := s.applyS3(); err != nil {
		slog.Warn("s3", "err", err)
	}
	return nil
}
