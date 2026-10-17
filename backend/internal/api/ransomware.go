package api

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/store"
	"strataos/internal/sys"
)

// Extensions written by common ransomware families.
var ransomExt = []string{".locked", ".encrypted", ".crypt", ".crypted", ".enc", ".wncry", ".wcry", ".locky", ".cerber", ".zepto", ".odin", ".crypz", ".cryp1", ".aaa", ".xyz", ".zzz", ".micro", ".ecc", ".ezz", ".exx", ".vvv", ".ttt", ".lockbit", ".conti", ".ryk", ".rapid", ".djvu", ".stop", ".phobos", ".makop", ".blackcat"}

func suspiciousName(name string) bool {
	n := strings.ToLower(name)
	for _, e := range ransomExt {
		if strings.HasSuffix(n, e) {
			return true
		}
	}
	return strings.Contains(n, "readme_decrypt") || strings.Contains(n, "how_to_decrypt") || strings.Contains(n, "restore_files") || strings.Contains(n, "decrypt_instructions")
}

// ransomWindow counts, per user, the renames/deletions of the last minute.
type ransomWindow struct {
	mu      sync.Mutex
	ops     map[string][]ransomOp
	alerted map[string]time.Time
}

type ransomOp struct {
	at  time.Time
	sus bool
}

func newRansomWindow() *ransomWindow {
	return &ransomWindow{ops: map[string][]ransomOp{}, alerted: map[string]time.Time{}}
}

// add records one operation and reports whether the user must be flagged.
func (w *ransomWindow) add(user, target string, now time.Time, threshold int) (bool, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cut := now.Add(-time.Minute)
	l := w.ops[user]
	i := 0
	for i < len(l) && l[i].at.Before(cut) {
		i++
	}
	l = append(l[i:], ransomOp{now, suspiciousName(target)})
	w.ops[user] = l
	if t, ok := w.alerted[user]; ok && now.Sub(t) < 10*time.Minute {
		return false, ""
	}
	sus := 0
	for _, o := range l {
		if o.sus {
			sus++
		}
	}
	reason := ""
	switch {
	case sus >= 10:
		reason = fmt.Sprintf("%d fichiers renommés avec une extension de rançongiciel en une minute (ex. %s)", sus, target)
	case len(l) >= threshold:
		reason = fmt.Sprintf("%d fichiers renommés ou supprimés en une minute", len(l))
	}
	if reason == "" {
		return false, ""
	}
	w.alerted[user] = now
	delete(w.ops, user)
	return true, reason
}

// parseAudit reads a full_audit line: user|ip|share|op|ok|path[|newpath].
func parseAudit(l string) (user, ip, share, target string, ok bool) {
	f := strings.Split(l, "|")
	if len(f) < 6 || (f[3] != "renameat" && f[3] != "unlinkat") || f[4] != "ok" {
		return "", "", "", "", false
	}
	target = f[5]
	if f[3] == "renameat" && len(f) >= 7 {
		target = f[6]
	}
	return f[0], f[1], f[2], target, f[0] != ""
}

// ransomLoop follows the Samba audit journal while the protection is on.
func (s *Server) ransomLoop() {
	w := newRansomWindow()
	for {
		var c store.Ransomware
		s.store.Read(func(st *store.State) { c = st.Ransom })
		if !c.Enabled || !sys.Has("journalctl") {
			time.Sleep(10 * time.Second)
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, "journalctl", "-f", "-n", "0", "-o", "cat", "SYSLOG_IDENTIFIER=smbd_audit")
		out, err := cmd.StdoutPipe()
		if err != nil || cmd.Start() != nil {
			cancel()
			time.Sleep(30 * time.Second)
			continue
		}
		// Stop following when the protection is turned off.
		go func() {
			for {
				time.Sleep(10 * time.Second)
				var on bool
				s.store.Read(func(st *store.State) { on = st.Ransom.Enabled })
				if !on {
					cancel()
					return
				}
			}
		}()
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			user, ip, share, target, ok := parseAudit(sc.Text())
			if !ok {
				continue
			}
			var threshold int
			s.store.Read(func(st *store.State) { threshold = st.Ransom.Threshold })
			if threshold <= 0 {
				threshold = 150
			}
			if flag, reason := w.add(user, target, time.Now(), threshold); flag {
				go s.ransomAlert(user, ip, share, reason)
			}
		}
		cancel()
		_ = cmd.Wait()
		time.Sleep(2 * time.Second)
	}
}

func (s *Server) ransomAlert(user, ip, share, reason string) {
	var block bool
	s.store.Read(func(st *store.State) { block = st.Ransom.Block })
	msg := fmt.Sprintf("Rançongiciel suspecté : le compte %s (%s) sur le partage %s : %s", user, ip, share, reason)
	if block {
		// Cut the running sessions first: every second counts.
		closeSMBSessions(user)
		_ = s.store.Update(func(st *store.State) error {
			if !slices.Contains(st.Ransom.Blocked, user) {
				st.Ransom.Blocked = append(st.Ransom.Blocked, user)
			}
			return nil
		})
		_ = s.applyShares()
		closeSMBSessions(user) // sessions opened meanwhile
		msg += ". Le compte est bloqué sur les partages : vérifiez le poste, puis débloquez-le dans Antivirus"
	}
	slog.Warn("ransomware suspected", "user", user, "ip", ip, "share", share)
	s.store.Log("error", "ransomware", user, msg)
	s.notify("Rançongiciel suspecté", msg)
}

// closeSMBSessions ends the Samba connections of a user.
func closeSMBSessions(user string) {
	out, err := sys.Run("smbstatus", "-p")
	if err != nil {
		return
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && f[1] == user {
			if pid, err := strconv.Atoi(f[0]); err == nil && pid > 1 {
				_, _ = sys.Run("smbcontrol", strconv.Itoa(pid), "shutdown")
			}
		}
	}
}

func (s *Server) ransomRoutes() {
	s.admin("GET /api/ransomware", func(r *http.Request) (any, error) {
		var c store.Ransomware
		s.store.Read(func(st *store.State) { c = st.Ransom })
		if c.Threshold == 0 {
			c.Threshold = 150
		}
		if c.Blocked == nil {
			c.Blocked = []string{}
		}
		return c, nil
	})
	s.admin("PUT /api/ransomware", func(r *http.Request) (any, error) {
		var req store.Ransomware
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Threshold == 0 {
			req.Threshold = 150
		}
		if req.Threshold < 5 || req.Threshold > 100000 {
			return nil, badRequest("invalid threshold")
		}
		err := s.store.Update(func(st *store.State) error {
			req.Blocked = st.Ransom.Blocked
			st.Ransom = req
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "ransomware", fmt.Sprintf("Ransomware protection: enabled=%v threshold=%d block=%v", req.Enabled, req.Threshold, req.Block))
		return nil, s.applyShares()
	})
	s.admin("POST /api/ransomware/unblock", func(r *http.Request) (any, error) {
		var req struct {
			User string `json:"user"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		err := s.store.Update(func(st *store.State) error {
			st.Ransom.Blocked = slices.DeleteFunc(st.Ransom.Blocked, func(u string) bool { return u == req.User })
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "ransomware", "Unblocked "+req.User)
		return nil, s.applyShares()
	})
}
