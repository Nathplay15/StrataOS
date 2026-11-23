package migrate

import (
	"context"
	"strings"
	"testing"

	"strataos/internal/store"
)

func TestValidate(t *testing.T) {
	good := store.Migration{Name: "Old NAS", Kind: "smb", Host: "nas.local", Share: "data", Path: "photos/2024", Dest: "/Main/old-nas", User: "bob", Password: "x"}
	if err := Validate(&good); err != nil {
		t.Fatalf("valid smb: %v", err)
	}
	if good.Path != "photos/2024" {
		t.Errorf("path normalized: %q", good.Path)
	}
	good2 := store.Migration{Name: "r", Kind: "rsync", Host: "192.168.1.10", Share: "backup", User: "rsync", Dest: "/Main/r"}
	if err := Validate(&good2); err != nil {
		t.Fatalf("valid rsync: %v", err)
	}
	// A ".." in the path is neutralized by the normalization, not followed.
	esc := store.Migration{Name: "x", Kind: "smb", Host: "h", Share: "s", Path: "../etc", Dest: "/A"}
	if err := Validate(&esc); err != nil || esc.Path != "etc" {
		t.Fatalf("path escape: err=%v path=%q", err, esc.Path)
	}
	for _, bad := range []store.Migration{
		{Name: "x", Kind: "ftp", Host: "h", Share: "s", Dest: "/A"},
		{Name: "", Kind: "smb", Host: "h", Share: "s", Dest: "/A"},
		{Name: "x", Kind: "smb", Host: "bad host!", Share: "s", Dest: "/A"},
		{Name: "x", Kind: "smb", Host: "h", Share: "a/b", Dest: "/A"},
		{Name: "x", Kind: "rsync", Host: "h", Share: "s", User: "", Dest: "/A"},
		{Name: "x", Kind: "smb", Host: "h", Share: "s", Port: 70000, Dest: "/A"},
		{Name: "x", Kind: "smb", Host: "h", Share: "s", Dest: "Main"},
		{Name: "x", Kind: "smb", Host: "h", Share: "s", Dest: "/A/../B"},
	} {
		if err := Validate(&bad); err == nil {
			t.Fatalf("invalid accepted: %+v", bad)
		}
	}
}

func TestBuild(t *testing.T) {
	m := store.Migration{ID: "m1", Name: "Old", Kind: "rsync", Host: "nas.local", Share: "backup", Path: "docs", Port: 0, User: "rsync", Password: "pw", Dest: "/Main/r", Delete: true}
	p, err := Build(m, "/srv/strata/main/r")
	if err != nil {
		t.Fatal(err)
	}
	if p.Src != "rsync://rsync@nas.local:873/backup/docs/" {
		t.Errorf("src = %q", p.Src)
	}
	if len(p.Env) != 1 || p.Env[0] != "RSYNC_PASSWORD=pw" {
		t.Errorf("env = %v (the password must go through the environment, not the command line)", p.Env)
	}
	args := strings.Join(p.RsyncArgs(), " ")
	if !strings.Contains(args, "--delete") || !strings.Contains(args, "-a --stats") || !strings.Contains(args, "/srv/strata/main/r/") {
		t.Errorf("args = %q", args)
	}
	if strings.Contains(args, "pw") {
		t.Error("the password must not be in the command line")
	}
	m.Port = 8730
	p, _ = Build(m, "/d")
	if !strings.Contains(p.Src, ":8730/") {
		t.Errorf("custom port: %q", p.Src)
	}

	smb := store.Migration{ID: "m2", Name: "s", Kind: "smb", Host: "nas.local", Share: "data", User: "bob", Password: "pw", Dest: "/Main/s"}
	p, err = Build(smb, "/srv/strata/main/s")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mount == "" || p.CredFile == "" || p.Src != p.Mount+"/" {
		t.Errorf("smb plan = %+v", p)
	}
	ma := strings.Join(p.MountArgs(smb), " ")
	if !strings.Contains(ma, "//nas.local/data") || !strings.Contains(ma, "ro,credentials=") || !strings.Contains(ma, "vers=3.0") {
		t.Errorf("mount args = %q", ma)
	}
	nfs := store.Migration{ID: "m3", Name: "n", Kind: "nfs", Host: "nas.local", Share: "export", Dest: "/Main/n"}
	p, _ = Build(nfs, "/d")
	if ma := strings.Join(p.MountArgs(nfs), " "); !strings.Contains(ma, "nas.local:/export") || !strings.Contains(ma, "-t nfs") {
		t.Errorf("nfs mount = %q", ma)
	}
}

func TestExecuteAndParseStats(t *testing.T) {
	oldM, oldU, oldR, oldW, oldDir := MountFn, UmountFn, RsyncFn, WriteCred, RunDir
	t.Cleanup(func() { MountFn, UmountFn, RsyncFn, WriteCred, RunDir = oldM, oldU, oldR, oldW, oldDir })
	RunDir = t.TempDir()
	var mounted, umounted, cred string
	var gotEnv, gotArgs []string
	MountFn = func(_ context.Context, args []string) error { mounted = strings.Join(args, " "); return nil }
	UmountFn = func(m string) error { umounted = m; return nil }
	WriteCred = func(path, user, password string) error { cred = user + "/" + password; return nil }
	RsyncFn = func(_ context.Context, env, args []string) (string, error) {
		gotEnv, gotArgs = env, args
		return "Number of regular files transferred: 42\nTotal transferred file size: 123456 bytes\n", nil
	}
	m := store.Migration{ID: "m1", Name: "s", Kind: "smb", Host: "nas", Share: "data", User: "bob", Password: "pw", Dest: "/Main/s"}
	p, err := Build(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out, err := Execute(context.Background(), m, p)
	if err != nil {
		t.Fatal(err)
	}
	if mounted == "" || umounted != p.Mount || cred != "bob/pw" {
		t.Errorf("mount=%q umount=%q cred=%q", mounted, umounted, cred)
	}
	if len(gotArgs) == 0 || gotArgs[len(gotArgs)-1] != p.Dest+"/" {
		t.Errorf("rsync args = %v", gotArgs)
	}
	files, bytes := ParseStats(out)
	if files != 42 || bytes != 123456 {
		t.Errorf("stats = %d %d", files, bytes)
	}
	// rsync kind: no mount.
	mounted, umounted, cred = "", "", ""
	m2 := store.Migration{ID: "m2", Name: "r", Kind: "rsync", Host: "nas", Share: "mod", User: "u", Password: "p", Dest: "/Main/r"}
	p2, _ := Build(m2, t.TempDir())
	if _, err := Execute(context.Background(), m2, p2); err != nil {
		t.Fatal(err)
	}
	if mounted != "" || umounted != "" || cred != "" {
		t.Errorf("rsync kind must not mount: %q %q %q", mounted, umounted, cred)
	}
	if len(gotEnv) != 1 || !strings.HasPrefix(gotEnv[0], "RSYNC_PASSWORD=") {
		t.Errorf("env = %v", gotEnv)
	}
}
