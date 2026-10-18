package api

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// reset forgets a user (after an unblock: the protection is armed again).
func (w *ransomWindow) reset(user string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.ops, user)
	delete(w.alerted, user)
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

var (
	ransomWake      = make(chan struct{}, 1) // settings changed
	ransomFollowing atomic.Bool              // the audit journal is being read
)

// ransomW counts the operations of every protocol.
var ransomW = newRansomWindow()

// ransomObserve records a rename (target = new name) or a deletion.
func (s *Server) ransomObserve(user, ip, share, target string) {
	var c store.Ransomware
	s.store.Read(func(st *store.State) { c = st.Ransom })
	if !c.Enabled || user == "" || slices.Contains(c.Blocked, user) {
		return
	}
	threshold := c.Threshold
	if threshold <= 0 {
		threshold = 150
	}
	if flag, reason := ransomW.add(user, target, time.Now(), threshold); flag {
		go s.ransomAlert(user, ip, share, reason)
	}
}

func (s *Server) ransomBlocked(user string) bool {
	var b bool
	s.store.Read(func(st *store.State) { b = slices.Contains(st.Ransom.Blocked, user) })
	return b
}

// ransomLoop follows the Samba audit journal while the protection is on.
func (s *Server) ransomLoop() {
	for {
		var c store.Ransomware
		s.store.Read(func(st *store.State) { c = st.Ransom })
		if !c.Enabled || !sys.Has("journalctl") {
			select {
			case <-ransomWake:
			case <-time.After(10 * time.Second):
			}
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		// From a moment ago rather than "-n 0": lines written while journalctl
		// opens the journal would be missed.
		since := fmt.Sprintf("@%d", time.Now().Add(-2*time.Second).Unix())
		cmd := exec.CommandContext(ctx, "journalctl", "-f", "--since", since, "-o", "cat", "SYSLOG_IDENTIFIER=smbd_audit")
		out, err := cmd.StdoutPipe()
		if err != nil || cmd.Start() != nil {
			cancel()
			time.Sleep(30 * time.Second)
			continue
		}
		ransomFollowing.Store(true)
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
			rememberSMB(user, ip, share)
			s.ransomObserve(user, ip, share, target)
		}
		cancel()
		_ = cmd.Wait()
		ransomFollowing.Store(false)
		time.Sleep(2 * time.Second)
	}
}

func (s *Server) ransomAlert(user, ip, share, reason string) {
	if user == nfsPseudoUser {
		s.pauseNFS(reason)
		return
	}
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
		// SFTP and FTP: lock the password and end the running sessions.
		_, _ = sys.Run("usermod", "-L", user)
		_, _ = sys.Run("pkill", "-KILL", "-u", user)
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
		frozen := []string{}
		s.store.Read(func(st *store.State) {
			for _, sh := range st.Shares {
				if sh.Frozen {
					frozen = append(frozen, sh.Name)
				}
			}
		})
		return struct {
			store.Ransomware
			Frozen []string `json:"frozen"`
		}{c, frozen}, nil
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
			req.Blocked, req.NFSPaused = st.Ransom.Blocked, st.Ransom.NFSPaused
			st.Ransom = req
			return nil
		})
		if err != nil {
			return nil, err
		}
		// Watch the audit journal before Samba starts writing it.
		select {
		case ransomWake <- struct{}{}:
		default:
		}
		select {
		case fanotifyWake <- struct{}{}:
		default:
		}
		wakeTraps()
		for i := 0; req.Enabled && (!ransomFollowing.Load() || !fanotifyReady.Load()) && i < 50; i++ {
			time.Sleep(100 * time.Millisecond)
		}
		s.audit(r, "ransomware", fmt.Sprintf("Ransomware protection: enabled=%v threshold=%d block=%v", req.Enabled, req.Threshold, req.Block))
		return nil, s.applyShares()
	})
	// Releases a share frozen by a trap file (its traps are put back).
	s.admin("POST /api/ransomware/unfreeze", func(r *http.Request) (any, error) {
		var req struct {
			Share string `json:"share"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var p string
		s.store.Read(func(st *store.State) {
			if sh := st.Share(req.Share); sh != nil && sh.Frozen {
				p = sh.Path
			}
		})
		if p == "" {
			return nil, notFound("frozen share not found")
		}
		// First keep the touched traps aside for the investigation (while the
		// share is still frozen, so that this does not count as an attack).
		for _, n := range trapNames {
			f := filepath.Join(p, n)
			if b, err := os.ReadFile(f); err != nil || string(b) != trapContent {
				_ = os.Rename(f, f+".modifie-"+time.Now().Format("20060102-150405"))
			}
		}
		err := s.store.Update(func(st *store.State) error {
			if sh := st.Share(req.Share); sh != nil {
				sh.Frozen = false
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "ransomware", "Share released: "+req.Share)
		wakeTraps()
		return nil, s.applyShares()
	})
	s.admin("POST /api/ransomware/nfs-resume", func(r *http.Request) (any, error) {
		_ = s.store.Update(func(st *store.State) error { st.Ransom.NFSPaused = false; return nil })
		ransomW.reset(nfsPseudoUser)
		s.audit(r, "ransomware", "NFS exports resumed")
		return nil, s.applyShares()
	})
	s.admin("POST /api/ransomware/unblock", func(r *http.Request) (any, error) {
		var req struct {
			User string `json:"user"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		disabled := false
		err := s.store.Update(func(st *store.State) error {
			st.Ransom.Blocked = slices.DeleteFunc(st.Ransom.Blocked, func(u string) bool { return u == req.User })
			if u := st.User(req.User); u != nil {
				disabled = u.Disabled
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if !disabled {
			_, _ = sys.Run("usermod", "-U", req.User)
		}
		ransomW.reset(req.User)
		s.audit(r, "ransomware", "Unblocked "+req.User)
		return nil, s.applyShares()
	})
}
