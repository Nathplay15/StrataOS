package backup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func TestValidatePull(t *testing.T) {
	ok := []store.BackupTask{
		{Name: "ssh", Kind: "rsync", SourceType: "ssh", Host: "nas2.local", User: "backup", Sources: []string{"/volume1/photos/", "/volume1/docs"}, DestPath: "/srv/strata/fast/nas2"},
		{Name: "rsyncd", Kind: "rsync", SourceType: "rsyncd", Host: "192.168.1.20", Port: 8873, Sources: []string{"photos", "/media/films/"}, DestPath: "/srv/strata/fast/nas2", RemotePassword: "s3cret"},
		{Name: "smb", Kind: "rsync", SourceType: "smb", Host: "diskstation", User: "alice smith", Domain: "WORKGROUP", Sources: []string{"Photos", "home$/alice"}, DestPath: "/srv/strata/fast/nas2", RemotePassword: "p,a=s s"},
		{Name: "guest", Kind: "rsync", SourceType: "smb", Host: "fe80::1", Sources: []string{"Public"}, DestPath: "/srv/strata/fast/pub", BWLimit: "2.5M", Versions: true},
		{Name: "zfs", Kind: "zfs", SourceType: "ssh", Host: "nas2", User: "root", Sources: []string{"tank/media"}, DestPath: "backup/nas2-media"},
	}
	for _, b := range ok {
		if err := Validate(&b); err != nil {
			t.Errorf("%s rejected: %v", b.Name, err)
		}
		if b.DestType != "local" {
			t.Errorf("%s: destination type %q", b.Name, b.DestType)
		}
	}
	base := func(m func(*store.BackupTask)) store.BackupTask {
		b := store.BackupTask{Name: "x", Kind: "rsync", SourceType: "ssh", Host: "h", User: "u", Sources: []string{"/data"}, DestPath: "/srv/strata/main/b"}
		m(&b)
		return b
	}
	bad := map[string]store.BackupTask{
		"host option injection":      base(func(b *store.BackupTask) { b.Host = "-oProxyCommand=id" }),
		"host with comma (mount -o)": base(func(b *store.BackupTask) { b.SourceType, b.Host, b.Sources = "smb", "h,uid=0", []string{"s"} }),
		"user injection":             base(func(b *store.BackupTask) { b.User = "-x" }),
		"relative remote path":       base(func(b *store.BackupTask) { b.Sources = []string{"data"} }),
		"remote root":                base(func(b *store.BackupTask) { b.Sources = []string{"/"} }),
		"dest outside volumes":       base(func(b *store.BackupTask) { b.DestPath = "/etc/cron.d" }),
		"same folder twice":          base(func(b *store.BackupTask) { b.Sources = []string{"/a/photos", "/b/photos"} }),
		"module with space":          base(func(b *store.BackupTask) { b.SourceType, b.Sources = "rsyncd", []string{"my module"} }),
		"module traversal":           base(func(b *store.BackupTask) { b.SourceType, b.Sources = "rsyncd", []string{"mod/../../etc"} }),
		"share traversal":            base(func(b *store.BackupTask) { b.SourceType, b.Sources = "smb", []string{"Photos/.."} }),
		"share with comma":           base(func(b *store.BackupTask) { b.SourceType, b.Sources = "smb", []string{"a,b"} }),
		"smb user with newline":      base(func(b *store.BackupTask) { b.SourceType, b.Sources, b.User = "smb", []string{"s"}, "a\npassword=x" }),
		"password with newline": base(func(b *store.BackupTask) {
			b.SourceType, b.Sources, b.RemotePassword = "smb", []string{"s"}, "a\ndomain=x"
		}),
		"bad domain":          base(func(b *store.BackupTask) { b.SourceType, b.Sources, b.Domain = "smb", []string{"s"}, "a/b" }),
		"bad bandwidth":       base(func(b *store.BackupTask) { b.BWLimit = "10M --delete" }),
		"unknown source type": base(func(b *store.BackupTask) { b.SourceType = "ftp" }),
		"restic cannot pull":  base(func(b *store.BackupTask) { b.Kind = "restic" }),
		"zfs remote dataset":  base(func(b *store.BackupTask) { b.Kind, b.Sources, b.DestPath = "zfs", []string{"tank/a;id"}, "backup/a" }),
		"zfs dest snapshot":   base(func(b *store.BackupTask) { b.Kind, b.Sources, b.DestPath = "zfs", []string{"tank/a"}, "backup/a@x" }),
		"zfs pull over rsyncd": base(func(b *store.BackupTask) {
			b.Kind, b.SourceType, b.Sources, b.DestPath = "zfs", "rsyncd", []string{"tank/a"}, "backup/a"
		}),
	}
	for name, b := range bad {
		if err := Validate(&b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	b := ok[0]
	_ = Validate(&b)
	if b.Sources[0] != "/volume1/photos" {
		t.Errorf("remote path not cleaned: %q", b.Sources[0])
	}
	v := ok[3]
	_ = Validate(&v)
	if v.KeepLast != 14 {
		t.Errorf("default version retention not applied: %d", v.KeepLast)
	}
	s := ok[0]
	s.RemotePassword = "x"
	_ = Validate(&s)
	if s.RemotePassword != "" {
		t.Error("SSH pulls must not keep a password (key only)")
	}
}

func TestPullArgs(t *testing.T) {
	KeyDir = "/k"
	ssh := store.BackupTask{SourceType: "ssh", Host: "nas2", User: "bk", Port: 2222, DestPath: "/srv/strata/fast/nas2", Delete: true, BWLimit: "5M", Excludes: []string{"*.tmp"}}
	args := pullArgs(&ssh, "/volume1/My Photos", "", "")
	n := len(args)
	if args[n-3] != "--" || args[n-2] != "bk@nas2:/volume1/My Photos/" || args[n-1] != "/srv/strata/fast/nas2/My Photos/" {
		t.Errorf("ssh source/dest: %q", args[n-3:])
	}
	for _, want := range []string{"--protect-args", "--delete", "--bwlimit=5M", "--exclude=*.tmp", "-aH", "--no-D", "--chmod=ug-s"} {
		if !slices.Contains(args, want) {
			t.Errorf("ssh args miss %s: %q", want, args)
		}
	}
	if i := slices.Index(args, "-e"); i < 0 || !strings.Contains(args[i+1], "-p 2222") || !strings.Contains(args[i+1], "BatchMode=yes") || !strings.Contains(args[i+1], "-i /k/id_ed25519") {
		t.Errorf("ssh command: %q", args)
	}

	d := store.BackupTask{SourceType: "rsyncd", Host: "fe80::2", User: "bk", DestPath: "/srv/strata/fast/d"}
	args = pullArgs(&d, "photos/2024", "", "/run/strata/backup/cred-1")
	n = len(args)
	if args[n-2] != "rsync://bk@[fe80::2]:873/photos/2024/" || args[n-1] != "/srv/strata/fast/d/2024/" {
		t.Errorf("rsyncd spec: %q", args[n-2:])
	}
	if !slices.Contains(args, "--password-file=/run/strata/backup/cred-1") || slices.Contains(args, "-e") {
		t.Errorf("rsyncd args: %q", args)
	}
	d.User = ""
	args = pullArgs(&d, "photos", "", "")
	if args[len(args)-2] != "rsync://[fe80::2]:873/photos/" || strings.Contains(strings.Join(args, " "), "password") {
		t.Errorf("anonymous rsyncd: %q", args)
	}

	m := store.BackupTask{SourceType: "smb", Host: "ds", DestPath: "/srv/strata/fast/s"}
	args = pullArgs(&m, "home$/alice", "/run/strata/backup/smb-1", "")
	n = len(args)
	if args[n-2] != "/run/strata/backup/smb-1/alice/" || args[n-1] != "/srv/strata/fast/s/alice/" || !slices.Contains(args, "-rlt") || slices.Contains(args, "-aH") {
		t.Errorf("smb args: %q", args)
	}
	args = pullArgs(&m, "Photos", "/run/strata/backup/smb-1", "")
	if args[len(args)-2] != "/run/strata/backup/smb-1/" {
		t.Errorf("smb share root: %q", args)
	}
}

func TestSMBMount(t *testing.T) {
	m := store.BackupTask{Host: "ds", User: "alice", RemotePassword: "Top-Secret", Domain: "OFFICE"}
	args := smbMountArgs(&m, "My Share", "/run/strata/backup/smb-1", "/run/strata/backup/cred-1")
	want := []string{"-t", "cifs", "//ds/My Share", "/run/strata/backup/smb-1", "-o", "ro,nosuid,nodev,noexec,iocharset=utf8,port=445,credentials=/run/strata/backup/cred-1"}
	if !slices.Equal(args, want) {
		t.Errorf("mount args:\n got %q\nwant %q", args, want)
	}
	if strings.Contains(strings.Join(args, " "), "Top-Secret") {
		t.Error("password on the command line")
	}
	if c := smbCredentials(&m); c != "username=alice\npassword=Top-Secret\ndomain=OFFICE\n" {
		t.Errorf("credentials: %q", c)
	}
	g := store.BackupTask{Host: "ds", Port: 1445}
	if a := smbMountArgs(&g, "Public", "/mp", "/c"); !strings.HasSuffix(a[5], "port=1445,guest") {
		t.Errorf("guest mount: %q", a)
	}
}

func TestSecretFile(t *testing.T) {
	SecretDir = filepath.Join(t.TempDir(), "secrets")
	p, rm, err := secretFile("pw\n")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v (%v)", fi.Mode(), err)
	}
	if di, _ := os.Stat(SecretDir); di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", di.Mode())
	}
	if b, _ := os.ReadFile(p); string(b) != "pw\n" {
		t.Errorf("content %q", b)
	}
	rm()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("not removed")
	}
}

func TestZFSPull(t *testing.T) {
	z := store.BackupTask{ID: "ab12", Sources: []string{"tank/my data"}, Recursive: true}
	if got := zfsPullSend(&z, "strata-pull-ab12-1", "strata-pull-ab12-2"); got != remotePath+`'zfs' 'send' '-R' '-I' 'tank/my data@strata-pull-ab12-1' 'tank/my data@strata-pull-ab12-2'` {
		t.Errorf("send: %s", got)
	}
	z.Recursive = false
	if got := zfsPullSend(&z, "", "s"); got != remotePath+`'zfs' 'send' 'tank/my data@s'` {
		t.Errorf("full send: %s", got)
	}
	list := "tank/a@strata-pull-ab12-20260102-000000\ntank/a@auto-x-1\ntank/a@strata-pull-ab12-20260101-000000\ntank/a@strata-pull-cd34-20260103-000000\n"
	if got := ownNames(list, pullPrefix(&z)); !slices.Equal(got, []string{"strata-pull-ab12-20260101-000000", "strata-pull-ab12-20260102-000000"}) {
		t.Errorf("own snapshots: %q", got)
	}
	p := store.BackupTask{Name: "z", Kind: "zfs", SourceType: "ssh", Host: "h", User: "u", Sources: []string{"tank/a"}, DestPath: "backup/a"}
	_ = Validate(&p)
	if p.KeepLast != 7 {
		t.Errorf("default keep: %d", p.KeepLast)
	}
}

func TestSync(t *testing.T) {
	KeyDir = "/k"
	s := store.BackupTask{ID: "s1", Name: "s", Kind: "sync", Sources: []string{"/srv/strata/main/Shared/"}, Host: "nas2", User: "bk", DestPath: "/srv/strata/main/Shared/", Delete: true, RemotePassword: "x"}
	if err := Validate(&s); err != nil {
		t.Fatal(err)
	}
	if s.DestType != "sftp" || s.Delete || s.RemotePassword != "" || s.KeepLast != 7 || s.Sources[0] != "/srv/strata/main/Shared" || s.DestPath != "/srv/strata/main/Shared" {
		t.Errorf("normalised: %+v", s)
	}
	SyncDir = "/sd"
	args := bisyncArgs(&s, "/sd/s1.conf", true)
	if !slices.Equal(args[:3], []string{"bisync", "/srv/strata/main/Shared", "strata:/srv/strata/main/Shared"}) || !slices.Contains(args, "--resync") || args[slices.Index(args, "--workdir")+1] != "/sd/bisync-s1" {
		t.Errorf("bisync args: %q", args)
	}
	if slices.Contains(bisyncArgs(&s, "/c", false), "--resync") {
		t.Error("--resync after the first run")
	}
	conf := rcloneConfig(&s)
	for _, want := range []string{"type = sftp\n", "host = nas2\n", "user = bk\n", "port = 22\n", "key_file = /k/id_ed25519\n", "known_hosts_file = /k/known_hosts\n"} {
		if !strings.Contains(conf, want) {
			t.Errorf("rclone config misses %q:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "pass") {
		t.Error("two-way sync must use the key only")
	}
	bad := map[string]store.BackupTask{
		"two folders":      {Name: "s", Kind: "sync", Sources: []string{"/srv/strata/a", "/srv/strata/b"}, Host: "h", User: "u", DestPath: "/x"},
		"local outside":    {Name: "s", Kind: "sync", Sources: []string{"/root"}, Host: "h", User: "u", DestPath: "/x"},
		"remote relative":  {Name: "s", Kind: "sync", Sources: []string{"/srv/strata/a"}, Host: "h", User: "u", DestPath: "x"},
		"remote root":      {Name: "s", Kind: "sync", Sources: []string{"/srv/strata/a"}, Host: "h", User: "u", DestPath: "/"},
		"host injection":   {Name: "s", Kind: "sync", Sources: []string{"/srv/strata/a"}, Host: "-oX", User: "u", DestPath: "/x"},
		"exclude flag":     {Name: "s", Kind: "sync", Sources: []string{"/srv/strata/a"}, Host: "h", User: "u", DestPath: "/x", Excludes: []string{"--delete-all"}},
		"newline in path":  {Name: "s", Kind: "sync", Sources: []string{"/srv/strata/a"}, Host: "h", User: "u", DestPath: "/x\n[evil]"},
		"negative retains": {Name: "s", Kind: "sync", Sources: []string{"/srv/strata/a"}, Host: "h", User: "u", DestPath: "/x", KeepLast: -1},
	}
	for name, b := range bad {
		if err := Validate(&b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSSHHost(t *testing.T) {
	for in, want := range map[string]string{"nas": "nas", "10.0.0.1": "10.0.0.1", "fe80::1": "[fe80::1]", "[::1]": "[::1]"} {
		if got := sshHost(in); got != want {
			t.Errorf("sshHost(%q) = %q", in, got)
		}
	}
}

const machineID = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>VerificationState</key>
	<integer>1</integer>
	<key>com.apple.backupd.HostUUID</key>
	<string>4C4C4544-0042-1010-8000-B2C04F4E3132</string>
	<key>com.apple.backupd.ModelID</key>
	<string>MacBookPro18,3</string>
	<key>VerificationDate</key>
	<date>2026-09-30T08:00:00Z</date>
</dict>
</plist>`

func TestTimeMachineBackups(t *testing.T) {
	dir := t.TempDir()
	b := filepath.Join(dir, "Alice’s MacBook Pro.sparsebundle")
	_ = os.MkdirAll(filepath.Join(b, "bands"), 0o755)
	_ = os.WriteFile(filepath.Join(b, "bands", "0"), make([]byte, 1000), 0o644)
	_ = os.WriteFile(filepath.Join(b, "com.apple.TimeMachine.MachineID.plist"), []byte(machineID), 0o644)
	old := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	_ = os.Chtimes(filepath.Join(b, "bands", "0"), old, old)
	_ = os.Chtimes(filepath.Join(b, "com.apple.TimeMachine.MachineID.plist"), old, old)
	// One level below (folder per user) and a stray folder.
	_ = os.MkdirAll(filepath.Join(dir, "bob", "iMac.sparsebundle"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bob", "iMac.sparsebundle", "com.apple.TimeMachine.SnapshotHistory.plist"),
		[]byte(`<plist><dict><key>Snapshots</key><array><dict><key>com.apple.backupd.SnapshotCompletionDate</key><date>2026-10-10T09:30:00Z</date></dict><dict><key>com.apple.backupd.SnapshotCompletionDate</key><date>2026-10-12T09:30:00Z</date></dict></array></dict></plist>`), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "other"), 0o755)
	_ = os.Symlink("/etc", filepath.Join(b, "bands", "link"))

	got := TimeMachineBackups(dir)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Name != "iMac" || !got[0].LastBackup.Equal(time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("history date not used: %+v", got[0])
	}
	a := got[1]
	if a.Name != "Alice’s MacBook Pro" || a.Model != "MacBookPro18,3" || a.MachineID != "4C4C4544-0042-1010-8000-B2C04F4E3132" || !a.LastBackup.Equal(old) {
		t.Errorf("bundle: %+v", a)
	}
	if a.Size < 1000 || a.Size > 2000 {
		t.Errorf("size %d (symlinks must not be followed)", a.Size)
	}
	if v := parsePlist(strings.NewReader("<plist><dict><key>a</key><true/><string>orphan</string></dict></plist>")); v == nil || v["a"] != "" {
		t.Errorf("a key without string value: %v", v)
	}
}
