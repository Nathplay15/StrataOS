// Package shares renders Samba and NFS configuration from StrataOS shares.
package shares

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"strataos/internal/accounts"
	"strataos/internal/store"
	"strataos/internal/sys"
)

const (
	smbConf       = "/etc/samba/smb.conf"
	smbSharesConf = "/etc/samba/strata-shares.conf"
	nfsExports    = "/etc/exports.d/strata.exports"
)

// ValidatePath ensures a share lives inside a StrataOS volume.
func ValidatePath(p string) (string, error) {
	clean := filepath.Clean(p)
	if !strings.HasPrefix(clean, "/srv/strata/") {
		return "", fmt.Errorf("shares must live under /srv/strata (inside a volume)")
	}
	// Quotes and line breaks would break smb.conf/exports lines, a trailing "\\"
	// continues an smb.conf line, and Samba expands %-variables in "path".
	if strings.ContainsAny(clean, "\n\r\"\\%") {
		return "", fmt.Errorf("invalid path (quotes, backslashes and %% are not allowed)")
	}
	return clean, nil
}

var (
	nfsClientRe  = regexp.MustCompile(`^[A-Za-z0-9*?.:\[\]_@/][A-Za-z0-9*?.:\[\]_@/-]{0,254}$`)
	nfsOptionsRe = regexp.MustCompile(`^[A-Za-z0-9_=:.,/@-]{0,512}$`)
)

// ValidateNFS checks the client list and option string of an NFS export. Both
// are written to /etc/exports.d: a client starting with "-" sets default options,
// and parentheses, quotes, spaces or line breaks would add exports of their own.
func ValidateNFS(clients, options string) error {
	for _, c := range strings.Fields(strings.ReplaceAll(clients, ",", " ")) {
		if !nfsClientRe.MatchString(c) {
			return fmt.Errorf("invalid NFS client %q", c)
		}
	}
	if !nfsOptionsRe.MatchString(options) || strings.HasPrefix(options, "-") {
		return fmt.Errorf("invalid NFS options %q", options)
	}
	return nil
}

// noSymlinkBelow checks that no existing component of p under /srv/strata is a
// symlink: users can create symlinks inside shares, and the root daemon must
// never re-permission whatever such a link points to.
func noSymlinkBelow(p string) error {
	cur := "/srv/strata"
	for _, part := range strings.Split(strings.TrimPrefix(p, cur+"/"), "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link: choose a real folder for the share", cur)
		}
	}
	return nil
}

// Prepare creates the share directory and applies permissions matching the share:
// the owning group (every StrataOS user) gets read-only access when no explicit
// permission is set and no access otherwise; named ACL entries grant the rest.
// This makes NFS and SSH honour the same rules Samba enforces.
func Prepare(sh *store.Share) error {
	if err := noSymlinkBelow(sh.Path); err != nil {
		return err
	}
	if err := os.MkdirAll(sh.Path, 0o775); err != nil {
		return err
	}
	// Work on a file descriptor opened without following symlinks, so a component
	// swapped for a link after the check above cannot redirect the changes.
	if err := noSymlinkBelow(sh.Path); err != nil {
		return err
	}
	fd, err := syscall.Open(sh.Path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open share folder: %w", err)
	}
	f := os.NewFile(uintptr(fd), sh.Path)
	defer f.Close()
	if g, err := user.LookupGroup(accounts.BaseGroup); err == nil {
		if gid, err := strconv.Atoi(g.Gid); err == nil {
			_ = f.Chown(-1, gid)
		}
	}
	groupBits := "---"
	if len(sh.Permissions) == 0 {
		groupBits = "r-x"
	}
	other := "---"
	if sh.Guest {
		other = "rwx"
	}
	if !sys.Has("setfacl") {
		mode := os.FileMode(0o700) | os.ModeSetgid
		if groupBits != "---" {
			mode |= 0o050
		}
		if sh.Guest {
			mode |= 0o007
		}
		return f.Chmod(mode)
	}
	_ = f.Chmod(0o770 | os.ModeSetgid)
	specs := []string{"u::rwx", "g::" + groupBits, "o::" + other, "d:u::rwx", "d:g::" + groupBits, "d:o::" + other}
	for who, perm := range sh.Permissions {
		bits := "---"
		if perm == "rw" && sh.QuotaFull {
			perm = "ro"
		}
		switch perm {
		case "rw":
			bits = "rwx"
		case "ro":
			bits = "r-x"
		}
		kind, name := "u", who
		if strings.HasPrefix(who, "@") {
			kind, name = "g", who[1:]
		}
		// Domain accounts exist only while the NAS is in the domain: an unknown
		// name would make setfacl reject every entry.
		if strings.Contains(name, `\`) && !resolvable(kind, name) {
			continue
		}
		specs = append(specs, fmt.Sprintf("%s:%s:%s", kind, name, bits), fmt.Sprintf("d:%s:%s:%s", kind, name, bits))
	}
	// Virtual machines may keep their disks or ISO images in a share: QEMU's
	// account may cross the share root (search only, it cannot list it).
	if _, err := user.Lookup("libvirt-qemu"); err == nil {
		specs = append(specs, "u:libvirt-qemu:--x")
	}
	specs = append(specs, "m::rwx", "d:m::rwx")
	// setfacl gets the already-open directory through /proc, never the raw path.
	byFD := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), f.Fd())
	if _, err := sys.Run("setfacl", "-b", "-k", byFD); err != nil {
		return err
	}
	_, err = sys.Run("setfacl", "-m", strings.Join(specs, ","), byFD)
	return err
}

func smbList(sh *store.Share, perm string) []string {
	var out []string
	for who, p := range sh.Permissions {
		if p == "rw" && sh.QuotaFull {
			p = "ro" // full share: read-only for everyone
		}
		if p == perm {
			out = append(out, who)
		}
	}
	sort.Strings(out)
	return out
}

// NetBIOSName is the host name as Windows sees it (upper case, 15 characters).
func NetBIOSName() string {
	h, _ := os.Hostname()
	h, _, _ = strings.Cut(h, ".")
	h = strings.ToUpper(clean(h))
	if len(h) > 15 {
		h = h[:15]
	}
	if h == "" {
		h = "STRATAOS"
	}
	return h
}

// resolvable asks NSS (getent): the static binary's os/user only reads
// /etc/passwd and /etc/group, not winbind or sssd.
func resolvable(kind, name string) bool {
	db := "passwd"
	if kind == "g" {
		db = "group"
	}
	_, err := sys.Run("getent", db, name)
	return err == nil
}

// smbNames formats a user list; domain names (DOMAIN\user, may contain spaces)
// are quoted.
func smbNames(l []string) string {
	out := make([]string, len(l))
	for i, n := range l {
		if strings.ContainsAny(n, " \\") {
			n = `"` + n + `"`
		}
		out[i] = n
	}
	return strings.Join(out, " ")
}

func smbGlobal(set store.Settings, dir store.Directory) string {
	wg := set.Workgroup
	if wg == "" {
		wg = "WORKGROUP"
	}
	ss := set.ServerString
	if ss == "" {
		ss = "StrataOS"
	}
	proto := set.SMBMinProto
	if proto == "" {
		proto = "SMB2_10"
	}
	role := "   server role = standalone server\n"
	netbios := "%h"
	if dir.Type == "ad" && !dir.JoinedAt.IsZero() {
		wg = dir.Workgroup
		// Member of an Active Directory domain: users and groups come from the
		// domain through winbind; their IDs are derived from the RIDs.
		role = fmt.Sprintf(`   security = ads
   realm = %s
   kerberos method = secrets and keytab
   winbind use default domain = no
   winbind enum users = yes
   winbind enum groups = yes
   winbind refresh tickets = yes
   template shell = /usr/sbin/nologin
   template homedir = /nonexistent
   idmap config * : backend = tdb
   idmap config * : range = 3000-7999
   idmap config %s : backend = rid
   idmap config %s : range = 100000-999999
`, dir.Realm, dir.Workgroup, dir.Workgroup)
		if dir.Server != "" {
			role += "   password server = " + clean(dir.Server) + "\n"
		}
		// A domain member needs a literal NetBIOS name: "%h" breaks the join.
		netbios = NetBIOSName()
	}
	return fmt.Sprintf(`# Managed by StrataOS - manual changes will be overwritten.
[global]
   workgroup = %s
   server string = %s
%s   netbios name = %s
   map to guest = Bad User
   guest account = nobody
   passdb backend = tdbsam
   obey pam restrictions = no
   unix password sync = no
   server min protocol = %s
   smb encrypt = if_required
   log file = /var/log/samba/log.%%m
   max log size = 1000
   logging = file
   load printers = no
   printing = bsd
   printcap name = /dev/null
   disable spoolss = yes
   usershare allow guests = no
   ea support = yes
   vfs objects = catia fruit streams_xattr
   fruit:metadata = stream
   fruit:model = MacSamba
   fruit:aapl = yes
   fruit:posix_rename = yes
   fruit:veto_appledouble = no
   fruit:wipe_intentionally_left_blank_rfork = yes
   fruit:delete_empty_adfiles = yes
   include = %s
`, wg, ss, role, netbios, proto, smbSharesConf)
}

// clean makes a value safe on a single smb.conf line: no line break, no section
// brackets, and no backslash (a trailing one continues the line).
func clean(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ", "[", "(", "]", ")", "\\", "/").Replace(s)
}

// SkippedError lists shares left out of the generated configuration (the rest was
// applied normally).
type SkippedError []string

func (e SkippedError) Error() string { return strings.Join(e, "; ") }

// Apply rewrites Samba and NFS configuration and reloads both services.
func Apply(st *store.State) error {
	var b strings.Builder
	b.WriteString("# Managed by StrataOS - manual changes will be overwritten.\n")
	var skipped []string
	for _, sh := range st.Shares {
		if !sh.SMB {
			continue
		}
		// Never publish a share whose folder is (or goes through) a symlink a user
		// could have planted: Samba would serve whatever it points to.
		if err := noSymlinkBelow(sh.Path); err != nil {
			skipped = append(skipped, fmt.Sprintf("share %s not published: %v", sh.Name, err))
			continue
		}
		fmt.Fprintf(&b, "\n[%s]\n", clean(sh.Name))
		fmt.Fprintf(&b, "   path = %s\n", sh.Path)
		if sh.Description != "" {
			fmt.Fprintf(&b, "   comment = %s\n", clean(sh.Description))
		}
		fmt.Fprintf(&b, "   browseable = %s\n", yesno(sh.Browseable))
		rw, ro := smbList(sh, "rw"), smbList(sh, "ro")
		valid := append(append([]string{}, rw...), ro...)
		if sh.Guest {
			b.WriteString("   guest ok = yes\n   read only = " + yesno(sh.QuotaFull) + "\n")
		} else {
			b.WriteString("   guest ok = no\n")
			if len(valid) == 0 {
				valid = []string{"@" + accounts.BaseGroup}
			}
			b.WriteString("   read only = yes\n")
			fmt.Fprintf(&b, "   valid users = %s\n", smbNames(valid))
			if len(rw) > 0 {
				fmt.Fprintf(&b, "   write list = %s\n", smbNames(rw))
			}
			denied := append(smbList(sh, "none"), st.Ransom.Blocked...)
			if len(denied) > 0 {
				fmt.Fprintf(&b, "   invalid users = %s\n", smbNames(denied))
			}
		}
		b.WriteString("   create mask = 0664\n   directory mask = 2775\n   inherit acls = yes\n   map acl inherit = yes\n")
		vfs := []string{"catia", "fruit", "streams_xattr"}
		if sh.RecycleBin {
			vfs = append(vfs, "recycle")
		}
		if st.Ransom.Enabled {
			vfs = append(vfs, "full_audit")
		}
		fmt.Fprintf(&b, "   vfs objects = %s\n", strings.Join(vfs, " "))
		if st.Ransom.Enabled {
			// Renames and deletions go to the journal (identifier smbd_audit),
			// where StrataOS watches for mass encryption.
			b.WriteString("   full_audit:prefix = %u|%I|%S\n   full_audit:success = renameat unlinkat\n   full_audit:failure = none\n   full_audit:facility = LOCAL5\n   full_audit:priority = NOTICE\n")
		}
		if sh.RecycleBin {
			b.WriteString("   recycle:repository = .recycle/%U\n   recycle:keeptree = yes\n   recycle:versions = yes\n   recycle:touch = yes\n   recycle:touch_mtime = yes\n   recycle:exclude = *.tmp,~$*\n   veto files = /.recycle/\n   hide files = /.recycle/\n")
		}
		if sh.TimeMachine {
			b.WriteString("   fruit:time machine = yes\n")
		}
	}
	if sys.Has("smbd") || dirExists("/etc/samba") {
		_ = os.MkdirAll("/etc/samba", 0o755)
		if err := ensureSmbConf(st.Settings, st.Directory); err != nil {
			return err
		}
		if err := sys.WriteFileAtomic(smbSharesConf, []byte(b.String()), 0o644); err != nil {
			return err
		}
		if sys.Has("testparm") {
			if _, err := sys.Run("testparm", "-s", smbConf); err != nil {
				return fmt.Errorf("invalid Samba configuration: %w", err)
			}
		}
		_, _ = sys.Run("systemctl", "reload-or-restart", "smbd")
	}

	var n strings.Builder
	n.WriteString("# Managed by StrataOS - manual changes will be overwritten.\n")
	for _, sh := range st.Shares {
		if !sh.NFS {
			continue
		}
		if err := ValidateNFS(sh.NFSClients, sh.NFSOptions); err != nil {
			skipped = append(skipped, fmt.Sprintf("share %s not exported: %v", sh.Name, err))
			continue
		}
		if err := noSymlinkBelow(sh.Path); err != nil {
			if sh.SMB {
				continue // already reported above
			}
			skipped = append(skipped, fmt.Sprintf("share %s not exported: %v", sh.Name, err))
			continue
		}
		clients := strings.Fields(strings.ReplaceAll(sh.NFSClients, ",", " "))
		if len(clients) == 0 {
			clients = []string{"*"}
		}
		opts := sh.NFSOptions
		if opts == "" {
			opts = "rw,sync,no_subtree_check,root_squash"
		}
		if sh.QuotaFull {
			opts = strings.Replace(","+opts+",", ",rw,", ",ro,", 1)
			opts = strings.Trim(opts, ",")
		}
		var parts []string
		for _, c := range clients {
			if strings.ContainsAny(c, "()\"") {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s(%s)", c, opts))
		}
		fmt.Fprintf(&n, "\"%s\" %s\n", sh.Path, strings.Join(parts, " "))
	}
	if sys.Has("exportfs") {
		_ = os.MkdirAll("/etc/exports.d", 0o755)
		if err := sys.WriteFileAtomic(nfsExports, []byte(n.String()), 0o644); err != nil {
			return err
		}
		if _, err := sys.Run("exportfs", "-ra"); err != nil {
			return fmt.Errorf("NFS exports: %w", err)
		}
	}
	if len(skipped) > 0 {
		return SkippedError(skipped)
	}
	return nil
}

func ensureSmbConf(set store.Settings, dir store.Directory) error {
	old, _ := os.ReadFile(smbConf)
	if len(old) > 0 && !strings.HasPrefix(string(old), "# Managed by StrataOS") {
		_ = os.WriteFile(smbConf+".pre-strata", old, 0o644)
	}
	return sys.WriteFileAtomic(smbConf, []byte(smbGlobal(set, dir)), 0o644)
}

func yesno(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// PurgeRecycle deletes files older than days from a share's .recycle folder.
// The walk never follows symlinks (users can create them), and a .recycle that
// is itself a symlink is ignored.
func PurgeRecycle(sharePath string, days int) (files int, freed int64) {
	root := filepath.Join(sharePath, ".recycle")
	if fi, err := os.Lstat(root); err != nil || !fi.IsDir() {
		return 0, 0
	}
	limit := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root {
				dirs = append(dirs, p)
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().After(limit) {
			return nil
		}
		if os.Remove(p) == nil {
			files++
			if d.Type().IsRegular() {
				freed += info.Size()
			}
		}
		return nil
	})
	// Remove now-empty folders, deepest first (Remove fails on non-empty ones).
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
	return files, freed
}

type Session struct {
	PID      string `json:"pid"`
	Username string `json:"username"`
	Machine  string `json:"machine"`
	Protocol string `json:"protocol"`
}

// SMBSessions lists connected Samba clients.
func SMBSessions() []Session {
	out := []Session{}
	if !sys.Has("smbstatus") {
		return out
	}
	res, err := sys.Run("smbstatus", "-b")
	if err != nil {
		return out
	}
	started := false
	for _, l := range strings.Split(res, "\n") {
		if strings.HasPrefix(l, "----") {
			started = true
			continue
		}
		f := strings.Fields(l)
		if !started || len(f) < 4 {
			continue
		}
		out = append(out, Session{PID: f[0], Username: f[1], Machine: f[3], Protocol: strings.Join(f[4:], " ")})
	}
	return out
}
