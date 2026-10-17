package api

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"strataos/internal/crypt"
	"strataos/internal/shares"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

var shareNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _\-.]{0,62}$`)

var reservedShares = map[string]bool{"global": true, "homes": true, "printers": true, "print$": true, "ipc$": true}

// applyShares regenerates Samba and NFS configuration; errors are logged.
func (s *Server) applyShares() error {
	var err error
	passive := s.haPassive()
	s.store.Read(func(st *store.State) {
		if passive {
			// High availability: the other NAS holds the shared address and
			// serves the shares; publish none here (the data is replicated).
			cp := *st
			cp.Shares = nil
			err = shares.Apply(&cp)
			return
		}
		err = shares.Apply(st)
	})
	var skipped shares.SkippedError
	if errors.As(err, &skipped) {
		// The configuration was applied without these shares: report, don't fail.
		slog.Warn("apply shares", "skipped", err)
		s.store.Log("warning", "shares", "", "Partages non publiés : "+err.Error())
		return nil
	}
	if err != nil {
		slog.Error("apply shares", "err", err)
		s.store.Log("error", "shares", "", "Échec de l'application de la configuration des partages : "+err.Error())
	}
	return err
}

var (
	localNameRe = regexp.MustCompile(`^@?[a-z_][a-z0-9_-]{0,31}$`)
	// DOMAIN\user or @DOMAIN\group (Active Directory names may contain spaces).
	domainNameRe = regexp.MustCompile(`^@?[A-Za-z0-9][A-Za-z0-9._-]{0,14}\\[^\\/:*?"<>|,@\x00-\x1f]{1,64}$`)
)

func validPerms(p map[string]string) error {
	for who, perm := range p {
		if perm != "rw" && perm != "ro" && perm != "none" {
			return badRequest("invalid permission for " + who)
		}
		if !localNameRe.MatchString(who) && !domainNameRe.MatchString(who) {
			return badRequest("invalid user or group " + who)
		}
	}
	return nil
}

func (s *Server) shareRoutes() {
	s.user("GET /api/shares", func(r *http.Request) (any, error) {
		var out []store.Share
		admin := isAdmin(r)
		me := userOf(r)
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				if admin || sh.Guest || sh.Permissions[me] == "rw" || sh.Permissions[me] == "ro" {
					out = append(out, *sh)
				}
			}
		})
		for i := range out {
			out[i].Locked = out[i].Encrypted && !crypt.Mounted(out[i].Path)
			if !admin {
				out[i].CipherDir = ""
			}
		}
		if out == nil {
			out = []store.Share{}
		}
		return out, nil
	})

	save := func(r *http.Request, create bool) (any, error) {
		var body struct {
			store.Share
			Passphrase string `json:"passphrase"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		req := body.Share
		req.Locked = false
		req.Name = strings.TrimSpace(req.Name)
		if !create {
			req.Name = r.PathValue("name")
		}
		if !shareNameRe.MatchString(req.Name) || reservedShares[strings.ToLower(req.Name)] {
			return nil, badRequest("invalid share name")
		}
		p, err := shares.ValidatePath(req.Path)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		req.Path = p
		if !s.pathOnVolume(p) {
			return nil, badRequest("the share path must be inside a mounted volume")
		}
		if req.Permissions == nil {
			req.Permissions = map[string]string{}
		}
		if req.QuotaGB < 0 || req.QuotaGB > 1e6 {
			return nil, badRequest("invalid quota")
		}
		req.QuotaFull, req.Used = false, 0
		if err := validPerms(req.Permissions); err != nil {
			return nil, err
		}
		if err := shares.ValidateNFS(req.NFSClients, req.NFSOptions); err != nil {
			return nil, badRequest(err.Error())
		}
		// Prepare (which refuses symlinked paths) must succeed before the share is
		// stored: a share saved despite an error would be published to Samba/NFS by
		// the next applyShares.
		var conflict error
		var existing store.Share
		s.store.Read(func(st *store.State) {
			conflict = shareConflict(st, req.Name, create)
			if e := st.Share(req.Name); e != nil {
				existing = *e
			}
		})
		if conflict != nil {
			return nil, conflict
		}
		if !create {
			// Encryption is chosen when the share is created.
			req.Encrypted, req.CipherDir = existing.Encrypted, existing.CipherDir
			if req.Encrypted && req.Path != existing.Path {
				return nil, badRequest("the folder of an encrypted share cannot be moved")
			}
		}
		if req.Encrypted && req.NFS {
			return nil, badRequest("encrypted shares cannot be exported over NFS")
		}
		recovery := ""
		if create && req.Encrypted {
			if !crypt.Installed() {
				return nil, badRequest("gocryptfs is not installed")
			}
			if ents, err := os.ReadDir(req.Path); err == nil && len(ents) > 0 {
				return nil, badRequest("an encrypted share needs a new or empty folder")
			}
			cipher, err := crypt.CipherDir(storage.MountOf(req.Path), req.Path)
			if err != nil {
				return nil, badRequest(err.Error())
			}
			if recovery, err = crypt.Init(cipher, body.Passphrase); err != nil {
				return nil, badRequest(err.Error())
			}
			if err := crypt.Unlock(cipher, req.Path, body.Passphrase, false); err != nil {
				_ = os.RemoveAll(cipher)
				return nil, err
			}
			req.CipherDir = cipher
		}
		if req.Encrypted && !crypt.Mounted(req.Path) && !create {
			// Locked: settings are saved, the folder is left alone.
		} else if err := shares.Prepare(&req); err != nil {
			if create && req.Encrypted {
				_ = crypt.Lock(req.Path)
				_ = os.RemoveAll(req.CipherDir)
			}
			return nil, badRequest(err.Error())
		}
		err = s.store.Update(func(st *store.State) error {
			if err := shareConflict(st, req.Name, create); err != nil {
				return err
			}
			existing := st.Share(req.Name)
			if create {
				req.CreatedAt = time.Now()
				c := req
				st.Shares = append(st.Shares, &c)
				return nil
			}
			req.CreatedAt = existing.CreatedAt
			// Measured by StrataOS, not by the form.
			req.Used, req.UsedAt = existing.Used, existing.UsedAt
			req.QuotaFull = existing.QuotaFull && req.QuotaBlock && req.QuotaGB > 0 && float64(existing.Used) >= req.QuotaGB*1e9
			*existing = req
			return nil
		})
		if err != nil {
			return nil, err
		}
		if err := s.applyShares(); err != nil {
			return nil, err
		}
		if req.QuotaGB > 0 || req.QuotaFull {
			go s.checkShareQuotas()
		}
		verb := "Updated"
		if create {
			verb = "Created"
		}
		s.audit(r, "shares", verb+" share "+req.Name)
		if recovery != "" {
			return map[string]any{"share": req, "recoveryKey": recovery}, nil
		}
		return req, nil
	}
	s.admin("POST /api/shares", func(r *http.Request) (any, error) { return save(r, true) })
	s.admin("PUT /api/shares/{name}", func(r *http.Request) (any, error) { return save(r, false) })

	s.admin("DELETE /api/shares/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		err := s.store.Update(func(st *store.State) error {
			sh := st.Share(name)
			if sh == nil {
				return notFound("share not found")
			}
			if sh.Encrypted {
				_ = crypt.Lock(sh.Path) // the encrypted data is kept in its hidden folder
			}
			st.Shares = slices.DeleteFunc(st.Shares, func(x *store.Share) bool { return x.Name == name })
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "shares", "Deleted share "+name+" (files kept)")
		return nil, s.applyShares()
	})

	// Encrypted shares: unlock with the passphrase or the recovery key, lock.
	s.admin("POST /api/shares/{name}/unlock", func(r *http.Request) (any, error) {
		var req struct {
			Passphrase  string `json:"passphrase"`
			RecoveryKey string `json:"recoveryKey"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var sh store.Share
		s.store.Read(func(st *store.State) {
			if p := st.Share(r.PathValue("name")); p != nil {
				sh = *p
			}
		})
		if !sh.Encrypted {
			return nil, notFound("encrypted share not found")
		}
		ip := clientIP(r)
		if !s.limiter.attempt(ip) {
			return nil, &Error{Status: http.StatusTooManyRequests, Msg: "too many attempts, try again later"}
		}
		secret, recovery := req.Passphrase, false
		if req.RecoveryKey != "" {
			secret, recovery = req.RecoveryKey, true
		}
		if err := crypt.Unlock(sh.CipherDir, sh.Path, secret, recovery); err != nil {
			s.audit(r, "shares", "Failed to unlock encrypted share "+sh.Name)
			return nil, badRequest(err.Error())
		}
		s.limiter.reset(ip)
		s.audit(r, "shares", "Unlocked encrypted share "+sh.Name)
		return nil, s.applyShares()
	})
	s.admin("POST /api/shares/{name}/lock", func(r *http.Request) (any, error) {
		var sh store.Share
		s.store.Read(func(st *store.State) {
			if p := st.Share(r.PathValue("name")); p != nil {
				sh = *p
			}
		})
		if !sh.Encrypted {
			return nil, notFound("encrypted share not found")
		}
		// Close the network sessions on the share, then unmount the clear view.
		_, _ = sys.Run("smbcontrol", "smbd", "close-share", sh.Name)
		if err := crypt.Lock(sh.Path); err != nil {
			return nil, badRequest(err.Error())
		}
		s.audit(r, "shares", "Locked encrypted share "+sh.Name)
		return nil, s.applyShares()
	})

	s.admin("GET /api/shares/sessions", func(r *http.Request) (any, error) { return shares.SMBSessions(), nil })
}

// shareConflict checks that a new share name is free, or that an updated share exists.
func shareConflict(st *store.State, name string, create bool) error {
	if !create {
		if st.Share(name) == nil {
			return notFound("share not found")
		}
		return nil
	}
	for _, sh := range st.Shares {
		if strings.EqualFold(sh.Name, name) {
			return badRequest("a share with this name already exists")
		}
	}
	return nil
}

// pathOnVolume reports whether p is inside a mounted volume or ZFS dataset.
func (s *Server) pathOnVolume(p string) bool {
	var ok bool
	s.store.Read(func(st *store.State) {
		for _, v := range st.Volumes {
			if (p == v.MountPoint || strings.HasPrefix(p, v.MountPoint+"/")) && storage.IsMountpoint(v.MountPoint) {
				ok = true
			}
		}
	})
	if ok {
		return true
	}
	if pools, _ := storage.ListZPools(false); pools != nil {
		for _, pl := range pools {
			mp := storage.MountRoot + "/" + pl.Name
			if p == mp || strings.HasPrefix(p, mp+"/") {
				return true
			}
		}
	}
	return false
}

// reapplySharePermissions re-runs share preparation at startup so permission fixes
// shipped by an upgrade reach existing shares. Shares whose volume is not mounted
// are skipped: creating their folder would write to the system disk instead.
func (s *Server) reapplySharePermissions() {
	var list []store.Share
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			list = append(list, *sh)
		}
	})
	for _, sh := range list {
		if !s.pathOnVolume(sh.Path) {
			continue
		}
		if err := shares.Prepare(&sh); err != nil {
			s.store.Log("warning", "shares", "", "Share "+sh.Name+": "+err.Error())
		}
	}
}

// lockedShareIn returns the name of a locked encrypted share holding one of
// the paths ("" when none).
func (s *Server) lockedShareIn(paths []string) string {
	var enc []store.Share
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			if sh.Encrypted {
				enc = append(enc, *sh)
			}
		}
	})
	for _, sh := range enc {
		for _, p := range paths {
			p = filepath.Clean(p)
			if (p == sh.Path || strings.HasPrefix(p, sh.Path+"/") || strings.HasPrefix(sh.Path, p+"/")) && !crypt.Mounted(sh.Path) {
				return sh.Name
			}
		}
	}
	return ""
}

// lockedSharesNotice reminds, after a start, which encrypted shares wait for
// their passphrase (they are not published until then).
func (s *Server) lockedSharesNotice() {
	time.Sleep(30 * time.Second)
	var names []string
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			if sh.Encrypted && !crypt.Mounted(sh.Path) {
				names = append(names, sh.Name)
			}
		}
	})
	if len(names) > 0 {
		msg := "Dossiers chiffrés à déverrouiller : " + strings.Join(names, ", ")
		s.store.Log("warning", "shares", "", msg)
		s.notify("Dossiers chiffrés verrouillés", msg)
	}
}
