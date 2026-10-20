package api

// Trap files and NFS: NFS writes come from kernel threads, so no account can
// be blamed. Decoy files placed in every share catch any client: touching one
// freezes the share (read-only everywhere) and, when Samba tells who did it,
// blocks the account. Mass renames or deletions made through NFS suspend the
// NFS exports.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"strataos/internal/crypt"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// Trap names sort first and last, where ransomware usually starts.
var trapNames = []string{"!0000 - DO NOT MODIFY (StrataOS).docx", "zzzz - DO NOT MODIFY (StrataOS).xlsx"}

const trapContent = "StrataOS trap file: do not modify, rename or delete it.\n" +
	"Any change makes the shared folder read-only (ransomware protection).\n"

// Trap files of StrataOS 0.4 (French names), removed while still untouched.
var legacyTrapNames = []string{"!0000 - NE PAS MODIFIER (StrataOS).docx", "zzzz - NE PAS MODIFIER (StrataOS).xlsx"}

const legacyTrapContent = "Fichier piège StrataOS : ne le modifiez pas, ne le renommez pas, ne le supprimez pas.\n" +
	"Toute modification passe le dossier partagé en lecture seule (protection contre les rançongiciels).\n" +
	"StrataOS trap file: do not modify, rename or delete it; doing so freezes the shared folder.\n"

// trapWake makes the trap loop look again (settings changed, share released).
var trapWake = make(chan struct{}, 1)

func wakeTraps() {
	select {
	case trapWake <- struct{}{}:
	default:
	}
}

func isTrap(name string) bool { return slices.Contains(trapNames, name) }

// recentSMB remembers the last audited operations, to tell who touched a trap.
var recentSMB struct {
	sync.Mutex
	ops []smbOp
}

type smbOp struct {
	at          time.Time
	user, share string
	ip          string
}

func rememberSMB(user, ip, share string) {
	recentSMB.Lock()
	defer recentSMB.Unlock()
	recentSMB.ops = append(recentSMB.ops, smbOp{time.Now(), user, share, ip})
	if len(recentSMB.ops) > 500 {
		recentSMB.ops = recentSMB.ops[len(recentSMB.ops)-500:]
	}
}

// whoTouched returns the SMB account that has the trap open or just renamed
// or deleted something in the share.
func whoTouched(share, file string) (string, string) {
	if out, err := sys.Run("smbstatus", "-L", "-n"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, file) {
				if f := strings.Fields(l); len(f) > 2 {
					if u := userOfUID(f[1]); u != "" {
						return u, "SMB"
					}
				}
			}
		}
	}
	recentSMB.Lock()
	defer recentSMB.Unlock()
	for i := len(recentSMB.ops) - 1; i >= 0; i-- {
		o := recentSMB.ops[i]
		if time.Since(o.at) > time.Minute {
			break
		}
		if o.share == share {
			return o.user, o.ip
		}
	}
	return "", ""
}

// trapWanted lists the trap files to keep, by share, and whether the traps
// are on at all.
func (s *Server) trapWanted() (map[string]string, bool) {
	want := map[string]string{} // file → share
	var c store.Ransomware
	var list []store.Share
	s.store.Read(func(st *store.State) {
		c = st.Ransom
		for _, sh := range st.Shares {
			list = append(list, *sh)
		}
	})
	if !c.Enabled || !c.Traps || s.haPassive() {
		return want, false
	}
	for _, sh := range list {
		if sh.Frozen || (sh.Encrypted && !crypt.Mounted(sh.Path)) || !s.pathOnVolume(sh.Path) {
			continue
		}
		for _, n := range trapNames {
			want[filepath.Join(sh.Path, n)] = sh.Name
		}
	}
	return want, true
}

func removeTraps(paths []string) {
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil && bytes.Equal(b, []byte(trapContent)) {
			_ = os.Remove(p)
		}
	}
}

// removeLegacyTraps deletes the untouched trap files of older versions from
// the folders that get traps now.
func removeLegacyTraps(want map[string]string) {
	dirs := map[string]bool{}
	for p := range want {
		dirs[filepath.Dir(p)] = true
	}
	for d := range dirs {
		for _, n := range legacyTrapNames {
			p := filepath.Join(d, n)
			if b, err := os.ReadFile(p); err == nil && bytes.Equal(b, []byte(legacyTrapContent)) {
				_ = os.Remove(p)
			}
		}
	}
}

// freezeShare makes a share read-only everywhere after a trap was touched.
// The account behind it (when Samba tells) is blocked first: every second
// counts while files are being encrypted.
func (s *Server) freezeShare(share, file string) {
	frozen := false
	s.store.Read(func(st *store.State) {
		if sh := st.Share(share); sh != nil {
			frozen = sh.Frozen
		}
	})
	if frozen {
		return
	}
	user, ip := whoTouched(share, file)
	var block bool
	s.store.Read(func(st *store.State) { block = st.Ransom.Block })
	if user != "" && block {
		s.ransomAlert(user, ip, share, "trap file modified")
	}
	_ = s.store.Update(func(st *store.State) error {
		if sh := st.Share(share); sh != nil {
			sh.Frozen = true
		}
		return nil
	})
	_ = s.applyShares()
	_, _ = sys.Run("smbcontrol", "smbd", "close-share", share)
	msg := fmt.Sprintf("Trap file modified in share %s: the share is now read-only. Check the computers, restore from the snapshots if needed, then release it in Antivirus", share)
	if user != "" {
		msg = fmt.Sprintf("Trap file modified in share %s by account %s (%s): the share is now read-only. Check the computers, restore from the snapshots if needed, then release it in Antivirus", share, user, ip)
	}
	s.store.Log("error", "ransomware", user, msg)
	s.notify("error", "Ransomware suspected", msg)
}

// pauseNFS suspends the NFS exports after mass changes through NFS.
func (s *Server) pauseNFS(reason string) {
	_ = s.store.Update(func(st *store.State) error { st.Ransom.NFSPaused = true; return nil })
	_ = s.applyShares()
	msg := "Ransomware suspected through NFS (" + reason + "): NFS shares are suspended. Check the NFS clients, then resume them in Antivirus"
	s.store.Log("error", "ransomware", "", msg)
	s.notify("error", "Ransomware suspected", msg)
}

// trapLoop keeps the trap files in place and watches them.
func (s *Server) trapLoop() {
	var watched map[string]string
	wds := map[int]string{}
	var closing *atomic.Bool // stops the current reader, which closes its descriptor
	stop := func() {
		if closing != nil {
			closing.Store(true)
			closing = nil
		}
		clear(wds)
	}
	events := make(chan string, 64)
	for {
		want, on := s.trapWanted()
		if !on {
			if watched != nil {
				// Turned off: take the traps out of every share (frozen ones too).
				stop()
				var old []string
				s.store.Read(func(st *store.State) {
					for _, sh := range st.Shares {
						for _, n := range trapNames {
							old = append(old, filepath.Join(sh.Path, n))
						}
					}
				})
				removeTraps(old)
				watched = nil
			}
			select {
			case <-time.After(30 * time.Second):
			case <-trapWake:
			}
			continue
		}
		// (Re)create missing traps, then watch them all again when the set changed.
		changed := len(want) != len(watched)
		for p, sh := range want {
			if watched[p] != sh {
				changed = true
			}
			if _, err := os.Lstat(p); err != nil {
				_ = os.WriteFile(p, []byte(trapContent), 0o664)
				changed = true
			}
		}
		if changed {
			stop()
			removeLegacyTraps(want)
			for len(events) > 0 { // events of files no longer watched this way
				<-events
			}
			if fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK); err == nil {
				for p := range want {
					if wd, err := syscall.InotifyAddWatch(fd, p, syscall.IN_MODIFY|syscall.IN_CLOSE_WRITE|syscall.IN_MOVE_SELF|syscall.IN_DELETE_SELF); err == nil {
						wds[wd] = p
					}
				}
				done := &atomic.Bool{}
				closing = done
				go func(fd int, wds map[int]string) {
					defer syscall.Close(fd)
					buf := make([]byte, 16*1024)
					for !done.Load() {
						if ready, _ := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 500); ready <= 0 {
							continue
						}
						n, err := syscall.Read(fd, buf)
						if err == syscall.EAGAIN || err == syscall.EINTR {
							continue
						}
						if err != nil || n <= 0 {
							return
						}
						for off := 0; off+syscall.SizeofInotifyEvent <= n; {
							ev := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
							off += syscall.SizeofInotifyEvent + int(ev.Len)
							if ev.Mask&syscall.IN_IGNORED != 0 {
								continue
							}
							if p, ok := wds[int(ev.Wd)]; ok {
								select {
								case events <- p:
								default:
								}
							}
						}
					}
				}(fd, copyWDs(wds))
			}
			watched = want
		}
		select {
		case p := <-events:
			if sh, ok := watched[p]; ok {
				s.freezeShare(sh, filepath.Base(p))
				delete(watched, p)
				// One write gives several events: the share is frozen already.
				time.Sleep(time.Second)
				for len(events) > 0 {
					<-events
				}
			}
		case <-time.After(30 * time.Second):
		case <-trapWake:
		}
	}
}

func copyWDs(m map[int]string) map[int]string {
	c := make(map[int]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}
