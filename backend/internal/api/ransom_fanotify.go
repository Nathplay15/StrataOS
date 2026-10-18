package api

// Renames and deletions made over SFTP and FTP are seen with fanotify on the
// volumes: the kernel reports the process behind each change, hence the
// account (sshd-session and vsftpd run as the user). Samba is followed by its
// audit log, WebDAV inside StrataOS.

import (
	"bytes"
	"encoding/binary"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// fileServiceComm maps the processes of the file services to their protocol.
var fileServiceComm = map[string]string{"sshd-session": "SFTP", "sshd": "SFTP", "sftp-server": "SFTP", "vsftpd": "FTP"}

// volumeMounts lists the file systems under the NAS storage.
func volumeMounts() []string {
	out, err := sys.Run("findmnt", "-rn", "-o", "TARGET")
	if err != nil {
		return nil
	}
	var l []string
	for _, m := range strings.Split(out, "\n") {
		m = strings.ReplaceAll(strings.TrimSpace(m), `\x20`, " ")
		if strings.HasPrefix(m, storage.MountRoot+"/") {
			l = append(l, m)
		}
	}
	slices.Sort(l)
	return l
}

var uidNames sync.Map // uid → user name

func userOfUID(uid string) string {
	if n, ok := uidNames.Load(uid); ok {
		return n.(string)
	}
	out, err := sys.Run("getent", "passwd", uid)
	if err != nil {
		return ""
	}
	name, _, _ := strings.Cut(strings.TrimSpace(out), ":")
	uidNames.Store(uid, name)
	return name
}

// processOwner returns the account and protocol of a file-service process.
func processOwner(pid int32) (user, proto string) {
	comm, err := os.ReadFile("/proc/" + strconv.Itoa(int(pid)) + "/comm")
	if err != nil {
		return "", ""
	}
	proto = fileServiceComm[strings.TrimSpace(string(comm))]
	if proto == "" {
		return "", ""
	}
	st, err := os.ReadFile("/proc/" + strconv.Itoa(int(pid)) + "/status")
	if err != nil {
		return "", ""
	}
	for _, l := range strings.Split(string(st), "\n") {
		if v, ok := strings.CutPrefix(l, "Uid:"); ok {
			f := strings.Fields(v)
			if len(f) > 1 && f[1] != "0" { // effective uid
				return userOfUID(f[1]), proto
			}
		}
	}
	return "", ""
}

// fanotifyName returns the entry name carried by a DFID_NAME info record.
func fanotifyName(info []byte) string {
	// header(4) fsid(8) handle_bytes(4) handle_type(4) handle[handle_bytes] name\0
	if len(info) < 20 || info[0] != unix.FAN_EVENT_INFO_TYPE_DFID_NAME {
		return ""
	}
	hb := int(binary.NativeEndian.Uint32(info[12:16]))
	start := 20 + hb
	if start >= len(info) {
		return ""
	}
	name := info[start:]
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	return string(name)
}

var (
	fanotifyWake  = make(chan struct{}, 1) // settings changed
	fanotifyReady atomic.Bool              // the volumes are watched
)

// ransomFileServicesLoop follows SFTP/FTP changes while the protection is on.
func (s *Server) ransomFileServicesLoop() {
	for {
		var on bool
		s.store.Read(func(st *store.State) { on = st.Ransom.Enabled })
		mounts := volumeMounts()
		if !on || len(mounts) == 0 {
			select {
			case <-fanotifyWake:
			case <-time.After(10 * time.Second):
			}
			continue
		}
		fd, err := unix.FanotifyInit(unix.FAN_CLASS_NOTIF|unix.FAN_CLOEXEC|unix.FAN_REPORT_DFID_NAME, unix.O_RDONLY|unix.O_LARGEFILE)
		if err != nil {
			time.Sleep(time.Minute)
			continue
		}
		marked := 0
		for _, m := range mounts {
			if unix.FanotifyMark(fd, unix.FAN_MARK_ADD|unix.FAN_MARK_FILESYSTEM, unix.FAN_MOVED_TO|unix.FAN_DELETE|unix.FAN_ONDIR, unix.AT_FDCWD, m) == nil {
				marked++
			}
		}
		if marked == 0 {
			unix.Close(fd)
			time.Sleep(time.Minute)
			continue
		}
		fanotifyReady.Store(true)
		// Stop when the protection is turned off or the volumes change.
		done := make(chan struct{})
		go func() {
			defer unix.Close(fd)
			for {
				select {
				case <-done:
					return
				case <-time.After(15 * time.Second):
				}
				var on bool
				s.store.Read(func(st *store.State) { on = st.Ransom.Enabled })
				if !on || !slices.Equal(volumeMounts(), mounts) {
					return
				}
			}
		}()
		buf := make([]byte, 64*1024)
		for {
			n, err := unix.Read(fd, buf)
			if err != nil || n <= 0 {
				if err == unix.EINTR {
					continue
				}
				break
			}
			for off := 0; off+unix.FAN_EVENT_METADATA_LEN <= n; {
				evLen := int(binary.NativeEndian.Uint32(buf[off:]))
				metaLen := int(binary.NativeEndian.Uint16(buf[off+6:]))
				if evLen < unix.FAN_EVENT_METADATA_LEN || off+evLen > n {
					break
				}
				pid := int32(binary.NativeEndian.Uint32(buf[off+20:]))
				name := ""
				if metaLen < evLen {
					name = fanotifyName(buf[off+metaLen : off+evLen])
				}
				off += evLen
				if user, proto := processOwner(pid); user != "" {
					s.ransomObserve(user, proto, proto, name)
				}
			}
		}
		close(done)
		fanotifyReady.Store(false)
		time.Sleep(2 * time.Second)
	}
}
