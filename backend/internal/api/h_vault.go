package api

// Backup vaults: this NAS keeps the restic repositories of other StrataOS NAS
// (restic REST protocol). Each paired NAS has its own folder and token; in
// append-only mode nothing can be deleted or overwritten, so a compromised
// NAS cannot destroy its backups.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/backup"
	"strataos/internal/ha"
	"strataos/internal/shares"
	"strataos/internal/store"
)

var (
	vaultTypes  = map[string]bool{"data": true, "keys": true, "locks": true, "snapshots": true, "index": true}
	vaultNameRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	vaultCodes  struct {
		sync.Mutex
		m map[string]vaultCode // code → vault
	}
)

type vaultCode struct {
	vault   string
	expires time.Time
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// vaultAuth finds the repository folder of an authenticated client.
func (s *Server) vaultAuth(r *http.Request, vid, cid string) (string, bool, error) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var dir string
	var appendOnly, ok bool
	s.store.Read(func(st *store.State) {
		for _, v := range st.Vaults {
			if v.ID != vid {
				continue
			}
			for _, c := range v.Clients {
				if c.ID == cid && subtle.ConstantTimeCompare([]byte(c.TokenHash), []byte(hashToken(tok))) == 1 {
					dir, appendOnly, ok = filepath.Join(v.Path, c.ID), v.AppendOnly, true
				}
			}
		}
	})
	if !ok {
		s.guardFail(clientIP(r), "backup vault", "")
		return "", false, &Error{Status: http.StatusUnauthorized, Msg: "authentication required"}
	}
	return dir, appendOnly, nil
}

func (s *Server) touchVaultClient(vid, cid string) {
	_ = s.store.Update(func(st *store.State) error {
		for _, v := range st.Vaults {
			for i := range v.Clients {
				if v.ID == vid && v.Clients[i].ID == cid && time.Since(v.Clients[i].LastSeen) > time.Minute {
					v.Clients[i].LastSeen = time.Now()
				}
			}
		}
		return nil
	})
}

// vaultFile maps a REST path (type/name) to a file of the repository.
func vaultFile(dir, typ, name string) string {
	if typ == "data" && len(name) >= 2 {
		return filepath.Join(dir, "data", name[:2], name)
	}
	return filepath.Join(dir, typ, name)
}

func (s *Server) vaultREST(w http.ResponseWriter, r *http.Request) {
	vid, cid := r.PathValue("vault"), r.PathValue("client")
	dir, appendOnly, err := s.vaultAuth(r, vid, cid)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.touchVaultClient(vid, cid)
	rest := strings.Trim(r.PathValue("rest"), "/")
	parts := strings.Split(rest, "/")
	switch {
	case rest == "" && r.Method == http.MethodPost: // ?create=true
		for t := range vaultTypes {
			if err := os.MkdirAll(filepath.Join(dir, t), 0o700); err != nil {
				writeErr(w, err)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	case rest == "config":
		file := filepath.Join(dir, "config")
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			http.ServeFile(w, r, file)
		case http.MethodPost:
			s.vaultWrite(w, r, file)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case len(parts) == 1 && vaultTypes[parts[0]] && r.Method == http.MethodGet:
		s.vaultList(w, r, dir, parts[0])
	case len(parts) == 2 && vaultTypes[parts[0]] && vaultNameRe.MatchString(parts[1]):
		file := vaultFile(dir, parts[0], parts[1])
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			f, err := os.Open(file)
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			defer f.Close()
			st, _ := f.Stat()
			w.Header().Set("Content-Type", "application/octet-stream")
			http.ServeContent(w, r, "", st.ModTime(), f)
		case http.MethodPost:
			s.vaultWrite(w, r, file)
		case http.MethodDelete:
			if appendOnly && parts[0] != "locks" {
				http.Error(w, "append-only vault: deletion refused", http.StatusForbidden)
				return
			}
			if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
				writeErr(w, err)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// vaultWrite stores a new file; existing files are never replaced.
func (s *Server) vaultWrite(w http.ResponseWriter, r *http.Request, file string) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)
	if _, err := os.Stat(file); err == nil {
		http.Error(w, "file exists", http.StatusForbidden)
		return
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		writeErr(w, err)
		return
	}
	tmp := file + ".tmp-" + newLinkID()
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		writeErr(w, err)
		return
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), r.Body)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	// Content-addressed files must match their name (config and locks aside).
	if base := filepath.Base(file); err == nil && vaultNameRe.MatchString(base) && !strings.Contains(file, "/locks/") && hex.EncodeToString(h.Sum(nil)) != base {
		err = fmt.Errorf("content does not match its name")
	}
	if err == nil {
		err = os.Link(tmp, file) // fails if it appeared meanwhile: never overwrite
	}
	_ = os.Remove(tmp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) vaultList(w http.ResponseWriter, r *http.Request, dir, typ string) {
	type entry struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	list := []entry{}
	_ = filepath.WalkDir(filepath.Join(dir, typ), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !vaultNameRe.MatchString(d.Name()) {
			return nil
		}
		if info, err := d.Info(); err == nil {
			list = append(list, entry{d.Name(), info.Size()})
		}
		return nil
	})
	if strings.Contains(r.Header.Get("Accept"), "application/vnd.x.restic.rest.v2") {
		w.Header().Set("Content-Type", "application/vnd.x.restic.rest.v2")
		_ = json.NewEncoder(w).Encode(list)
		return
	}
	names := make([]string, 0, len(list))
	for _, e := range list {
		names = append(names, e.Name)
	}
	w.Header().Set("Content-Type", "application/vnd.x.restic.rest.v1")
	_ = json.NewEncoder(w).Encode(names)
}

func dirSize(p string) int64 {
	var n int64
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if i, err := d.Info(); err == nil {
				n += i.Size()
			}
		}
		return nil
	})
	return n
}

func (s *Server) vaultRoutes() {
	s.mux.HandleFunc("/api/vault/{vault}/{client}/{rest...}", s.vaultREST)

	// ---- On the vault NAS ----
	s.admin("GET /api/vaults", func(r *http.Request) (any, error) {
		var out []store.Vault
		s.store.Read(func(st *store.State) {
			for _, v := range st.Vaults {
				c := *v
				c.Clients = slices.Clone(v.Clients)
				out = append(out, c)
			}
		})
		for i := range out {
			for j := range out[i].Clients {
				out[i].Clients[j].TokenHash = ""
				out[i].Clients[j].Size = dirSize(filepath.Join(out[i].Path, out[i].Clients[j].ID))
			}
		}
		if out == nil {
			out = []store.Vault{}
		}
		return out, nil
	})
	s.admin("POST /api/vaults", func(r *http.Request) (any, error) {
		var req struct {
			Name       string `json:"name"`
			Path       string `json:"path"`
			AppendOnly bool   `json:"appendOnly"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" || len(req.Name) > 60 {
			return nil, badRequest("invalid name")
		}
		p, err := shares.ValidatePath(req.Path)
		if err != nil || !s.pathOnVolume(p) {
			return nil, badRequest("the vault folder must be inside a mounted volume")
		}
		if err := os.MkdirAll(p, 0o700); err != nil {
			return nil, err
		}
		_ = os.Chmod(p, 0o700)
		v := &store.Vault{ID: newLinkID()[:12], Name: req.Name, Path: p, AppendOnly: req.AppendOnly, Created: time.Now(), Clients: []store.VaultClient{}}
		err = s.store.Update(func(st *store.State) error { st.Vaults = append(st.Vaults, v); return nil })
		s.audit(r, "vault", "Backup vault created: "+p)
		return v, err
	})
	s.admin("DELETE /api/vaults/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		err := s.store.Update(func(st *store.State) error {
			st.Vaults = slices.DeleteFunc(st.Vaults, func(v *store.Vault) bool { return v.ID == id })
			return nil
		})
		s.audit(r, "vault", "Backup vault removed (data kept)")
		return nil, err
	})
	s.admin("DELETE /api/vaults/{id}/clients/{cid}", func(r *http.Request) (any, error) {
		id, cid := r.PathValue("id"), r.PathValue("cid")
		err := s.store.Update(func(st *store.State) error {
			for _, v := range st.Vaults {
				if v.ID == id {
					v.Clients = slices.DeleteFunc(v.Clients, func(c store.VaultClient) bool { return c.ID == cid })
				}
			}
			return nil
		})
		s.audit(r, "vault", "Vault access revoked for a NAS (backups kept)")
		return nil, err
	})
	s.admin("POST /api/vaults/{id}/code", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		found := false
		s.store.Read(func(st *store.State) {
			for _, v := range st.Vaults {
				found = found || v.ID == id
			}
		})
		if !found {
			return nil, notFound("vault not found")
		}
		code := pairingCode()
		vaultCodes.Lock()
		if vaultCodes.m == nil {
			vaultCodes.m = map[string]vaultCode{}
		}
		vaultCodes.m[code] = vaultCode{id, time.Now().Add(15 * time.Minute)}
		vaultCodes.Unlock()
		return map[string]any{"code": code, "expires": time.Now().Add(15 * time.Minute)}, nil
	})

	// Called by the NAS that will send its backups (the code proves the consent).
	s.mux.HandleFunc("POST /api/vault-join", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		ip := clientIP(r)
		if !s.limiter.attempt(ip) {
			writeErr(w, &Error{Status: http.StatusTooManyRequests, Msg: "too many attempts, try again later"})
			return
		}
		var req struct {
			Code string `json:"code"`
			Name string `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		code := strings.ToUpper(strings.TrimSpace(req.Code))
		vaultCodes.Lock()
		vc, ok := vaultCodes.m[code]
		if ok {
			delete(vaultCodes.m, code) // single use
		}
		vaultCodes.Unlock()
		if !ok || time.Now().After(vc.expires) {
			s.guardFail(ip, "backup vault", "")
			writeErr(w, &Error{Status: http.StatusForbidden, Msg: "wrong or expired pairing code"})
			return
		}
		s.limiter.reset(ip)
		name := strings.TrimSpace(req.Name)
		if name == "" || len(name) > 64 {
			name = ip
		}
		token := randHex(32)
		client := store.VaultClient{ID: newLinkID()[:12], Name: name, TokenHash: hashToken(token), Created: time.Now()}
		var appendOnly bool
		err := s.store.Update(func(st *store.State) error {
			for _, v := range st.Vaults {
				if v.ID == vc.vault {
					v.Clients = append(v.Clients, client)
					appendOnly = v.AppendOnly
					return nil
				}
			}
			return notFound("vault not found")
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		s.store.Log("info", "vault", "", fmt.Sprintf("The NAS %s can now send its backups to the vault", name))
		writeJSON(w, http.StatusOK, map[string]any{"path": "/api/vault/" + vc.vault + "/" + client.ID + "/", "token": token, "appendOnly": appendOnly})
	})

	// ---- On the NAS that sends its backups ----
	s.admin("POST /api/backups/vault-pair", func(r *http.Request) (any, error) {
		var req struct {
			Host string `json:"host"`
			Code string `json:"code"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		host := strings.TrimSpace(req.Host)
		if host == "" || strings.ContainsAny(host, "/ @?#") {
			return nil, badRequest("invalid address")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		fp, err := ha.CertFingerprint(ctx, host)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		base := "https://" + bracketHost(host) + ":7443"
		me, _ := hostname()
		body, _ := json.Marshal(map[string]string{"code": req.Code, "name": me})
		hreq, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/vault-join", strings.NewReader(string(body)))
		hreq.Header.Set("Content-Type", "application/json")
		hreq.Header.Set("X-Strata", "1")
		resp, err := (&http.Client{Transport: backup.PinnedTransport(fp), Timeout: 30 * time.Second}).Do(hreq)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		defer resp.Body.Close()
		var res struct {
			Path       string `json:"path"`
			Token      string `json:"token"`
			AppendOnly bool   `json:"appendOnly"`
			Error      string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&res)
		if resp.StatusCode != http.StatusOK {
			return nil, badRequest(firstNonEmpty(res.Error, resp.Status))
		}
		s.audit(r, "backup", "Paired with the backup vault of "+host)
		return map[string]any{"vaultUrl": base + res.Path, "vaultToken": res.Token, "vaultCert": fp, "appendOnly": res.AppendOnly}, nil
	})
}

func bracketHost(h string) string {
	if strings.Contains(h, ":") {
		return "[" + h + "]"
	}
	return h
}

// pairingCode is a one-time code such as 3F9A2-C41B0.
func pairingCode() string {
	c := strings.ToUpper(randHex(5))
	return c[:5] + "-" + c[5:]
}
