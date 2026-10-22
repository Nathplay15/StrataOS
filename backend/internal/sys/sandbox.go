package sys

import (
	"os"
	"os/user"
	"strconv"
	"sync"
	"syscall"
)

// SandboxUser is the account that runs the converters and decoders of user
// files (ffmpeg, LibreOffice, libvips, 7-Zip): a flaw in one of them, fed a
// crafted file, then cannot touch the system or other users' data.
const SandboxUser = "strata-preview"

type sandboxIDs struct {
	uid, gid uint32
	groups   []uint32
	ok       bool
}

var sandbox = sync.OnceValue(func() sandboxIDs {
	if os.Geteuid() != 0 {
		return sandboxIDs{} // unit tests and development: nothing to drop
	}
	if _, err := user.Lookup(SandboxUser); err != nil {
		_, _ = Run("useradd", "--system", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", "--user-group", SandboxUser)
	}
	u, err := user.Lookup(SandboxUser)
	if err != nil {
		u, err = user.Lookup("nobody")
	}
	if err != nil {
		return sandboxIDs{uid: 65534, gid: 65534, ok: true}
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	ids := sandboxIDs{uid: uint32(uid), gid: uint32(gid), ok: true}
	// Video acceleration: the render nodes belong to these groups.
	for _, g := range []string{"render", "video"} {
		if gr, err := user.LookupGroup(g); err == nil {
			if n, err := strconv.Atoi(gr.Gid); err == nil {
				ids.groups = append(ids.groups, uint32(n))
			}
		}
	}
	return ids
})

// Sandboxed returns process attributes that run a command as SandboxUser (when
// the daemon runs as root), in its own process group, killed with the daemon.
func Sandboxed() *syscall.SysProcAttr {
	a := &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if ids := sandbox(); ids.ok {
		a.Credential = &syscall.Credential{Uid: ids.uid, Gid: ids.gid, Groups: ids.groups}
	}
	return a
}

// SandboxOwner returns the uid and gid of SandboxUser (-1 when not dropping
// privileges), for the private work folders given to sandboxed commands.
func SandboxOwner() (int, int) {
	if ids := sandbox(); ids.ok {
		return int(ids.uid), int(ids.gid)
	}
	return -1, -1
}
