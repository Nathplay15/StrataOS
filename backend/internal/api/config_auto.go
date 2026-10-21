package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"strataos/internal/confbackup"
	"strataos/internal/jobs"
	"strataos/internal/shares"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// configFileRe matches the files written by the automatic backups (and only
// them: the pruning never deletes anything else in the folder).
var configFileRe = regexp.MustCompile(`^strataos-config-[A-Za-z0-9.-]{0,64}-[0-9]{8}-[0-9]{6}\.json$`)

// saveConfigAuto validates and stores the automatic backup settings.
func (s *Server) saveConfigAuto(c store.ConfigBackup) error {
	if c.Keep <= 0 {
		c.Keep = 7
	}
	if c.Keep > 365 || c.Hour < 0 || c.Hour > 23 {
		return badRequest("invalid number of copies or hour")
	}
	if c.Enabled || c.Folder != "" {
		p, err := shares.ValidatePath(c.Folder)
		if err != nil || !s.pathOnVolume(p) {
			return badRequest("the folder must be inside a mounted volume")
		}
		c.Folder = p
	}
	if c.Secrets && len(c.Passphrase) < confbackup.MinPassphrase {
		return badRequest(fmt.Sprintf("the passphrase must be at least %d characters", confbackup.MinPassphrase))
	}
	if !c.Secrets {
		c.Passphrase = ""
	}
	return s.store.Update(func(st *store.State) error {
		c.LastRun, c.LastResult, c.LastFile = st.ConfigBackup.LastRun, st.ConfigBackup.LastResult, st.ConfigBackup.LastFile
		st.ConfigBackup = c
		return nil
	})
}

// safeFolder creates the folder when needed and refuses symbolic links on
// the way (the daemon writes there as root).
func safeFolder(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if real != dir {
		return fmt.Errorf("%s goes through a symbolic link", dir)
	}
	return nil
}

// configBackupFiles lists the automatic backups of a folder, newest first.
func configBackupFiles(dir string) []os.FileInfo {
	ents, _ := os.ReadDir(dir)
	var out []os.FileInfo
	for _, e := range ents {
		if !configFileRe.MatchString(e.Name()) || !e.Type().IsRegular() {
			continue
		}
		if fi, err := e.Info(); err == nil {
			out = append(out, fi)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() > out[j].Name() })
	return out
}

// runConfigBackup writes one export into the folder and keeps the newest copies.
func (s *Server) runConfigBackup() (string, error) {
	var c store.ConfigBackup
	s.store.Read(func(st *store.State) { c = st.ConfigBackup })
	file, err := func() (string, error) {
		if c.Folder == "" || !s.pathOnVolume(c.Folder) {
			return "", fmt.Errorf("the folder %s is not on a mounted volume", c.Folder)
		}
		if err := safeFolder(c.Folder); err != nil {
			return "", err
		}
		d, err := s.exportConfig(c.Secrets, c.Passphrase)
		if err != nil {
			return "", err
		}
		data, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			return "", err
		}
		host := regexp.MustCompile(`[^A-Za-z0-9.-]`).ReplaceAllString(d.Hostname, "")
		if len(host) > 64 {
			host = host[:64]
		}
		name := filepath.Join(c.Folder, fmt.Sprintf("strataos-config-%s-%s.json", host, time.Now().Format("20060102-150405")))
		if err := sys.WriteFileAtomic(name, data, 0o640); err != nil {
			return "", err
		}
		// Readable by the owner of the folder (e.g. through its share).
		if fi, err := os.Stat(c.Folder); err == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				_ = os.Lchown(name, int(st.Uid), int(st.Gid))
			}
		}
		keep := max(c.Keep, 1)
		for i, fi := range configBackupFiles(c.Folder) {
			if i >= keep {
				_ = os.Remove(filepath.Join(c.Folder, fi.Name()))
			}
		}
		return name, nil
	}()
	result := "OK"
	if err != nil {
		result = err.Error()
	}
	_ = s.store.Update(func(st *store.State) error {
		st.ConfigBackup.LastRun, st.ConfigBackup.LastResult = time.Now(), result
		if file != "" {
			st.ConfigBackup.LastFile = file
		}
		return nil
	})
	if err != nil {
		msg := "Configuration backup failed: " + err.Error()
		s.store.Log("error", "config", "", msg)
		s.notify("error", "Configuration backup failed", msg)
	} else {
		s.store.Log("info", "config", "", "Configuration saved to "+file)
	}
	return file, err
}

// configAutoLoop runs the daily export once the chosen hour is reached (and
// catches up after the NAS was off at that time).
func (s *Server) configAutoLoop() {
	time.Sleep(2 * time.Minute)
	for {
		var c store.ConfigBackup
		s.store.Read(func(st *store.State) { c = st.ConfigBackup })
		now := time.Now()
		slot := time.Date(now.Year(), now.Month(), now.Day(), c.Hour, 0, 0, 0, now.Location())
		if c.Enabled && !now.Before(slot) && c.LastRun.Before(slot) {
			_, _ = s.runConfigBackup()
		}
		time.Sleep(5 * time.Minute)
	}
}

func (s *Server) configAutoRoutes() {
	s.admin("GET /api/config/auto", func(r *http.Request) (any, error) {
		var c store.ConfigBackup
		s.store.Read(func(st *store.State) { c = st.ConfigBackup })
		if c.Passphrase != "" {
			c.Passphrase = masked
		}
		if c.Keep == 0 {
			c.Keep, c.Hour = 7, 2
		}
		type file struct {
			Name     string    `json:"name"`
			Size     int64     `json:"size"`
			Modified time.Time `json:"modified"`
		}
		files := []file{}
		if c.Folder != "" && s.pathOnVolume(c.Folder) {
			for _, fi := range configBackupFiles(c.Folder) {
				files = append(files, file{fi.Name(), fi.Size(), fi.ModTime()})
			}
		}
		return map[string]any{"config": c, "files": files}, nil
	})
	s.admin("PUT /api/config/auto", func(r *http.Request) (any, error) {
		var req store.ConfigBackup
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Folder = strings.TrimSpace(req.Folder)
		if req.Passphrase == masked {
			s.store.Read(func(st *store.State) { req.Passphrase = st.ConfigBackup.Passphrase })
		}
		if err := s.saveConfigAuto(req); err != nil {
			return nil, err
		}
		s.audit(r, "config", fmt.Sprintf("Automatic configuration backups: enabled=%v folder=%s", req.Enabled, req.Folder))
		return nil, nil
	})
	s.admin("POST /api/config/auto/run", func(r *http.Request) (any, error) {
		return s.jobs.Start("config", "Configuration backup", userOf(r), func(ctx context.Context, j *jobs.Job) error {
			file, err := s.runConfigBackup()
			if err == nil {
				j.Logf("%s", file)
			}
			return err
		}), nil
	})
	// Downloads one of the automatic backups.
	s.adminRaw("GET /api/config/auto/files/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		var dir string
		s.store.Read(func(st *store.State) { dir = st.ConfigBackup.Folder })
		if !configFileRe.MatchString(name) || dir == "" {
			writeErr(w, notFound("file not found"))
			return
		}
		path := filepath.Join(dir, name)
		if li, err := os.Lstat(path); err != nil || !li.Mode().IsRegular() {
			writeErr(w, notFound("file not found"))
			return
		}
		f, err := os.Open(path)
		if err != nil {
			writeErr(w, notFound("file not found"))
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			writeErr(w, notFound("file not found"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		http.ServeContent(w, r, name, fi.ModTime(), f)
	})
}
