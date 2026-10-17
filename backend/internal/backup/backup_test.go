package backup

import (
	"os/exec"
	"strings"
	"testing"

	"strataos/internal/store"
)

func TestShellQuote(t *testing.T) {
	for _, s := range []string{"tank/backup", "a b", "it's", "$(touch /tmp/pwned)", "`id`", "x;rm -rf /", `"q"`} {
		out, err := exec.Command("sh", "-c", "printf %s "+shellQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("shellQuote(%q) round-trip gave %q (%v)", s, out, err)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := []store.BackupTask{
		{Name: "a", Kind: "rsync", Sources: []string{"/srv/strata/main/Photos"}, DestType: "local", DestPath: "/srv/strata/usb/backup"},
		{Name: "b", Kind: "rsync", Sources: []string{"/srv/strata/main/Photos"}, DestType: "ssh", Host: "nas2.local", User: "backup", DestPath: "/volume1/backup"},
		{Name: "c", Kind: "restic", Sources: []string{"/srv/strata/main"}, DestType: "s3", S3Endpoint: "https://s3.example.com", DestPath: "bucket/nas", AccessKey: "k", SecretKey: "s", Password: "longenough"},
		{Name: "d", Kind: "zfs", Sources: []string{"tank/media"}, DestType: "local", DestPath: "backup/media"},
	}
	for _, b := range ok {
		if err := Validate(&b); err != nil {
			t.Errorf("%s rejected: %v", b.Name, err)
		}
	}
	bad := map[string]store.BackupTask{
		"source outside volumes":     {Name: "x", Kind: "rsync", Sources: []string{"/etc"}, DestType: "local", DestPath: "/srv/strata/b"},
		"dest inside source":         {Name: "x", Kind: "rsync", Sources: []string{"/srv/strata/main"}, DestType: "local", DestPath: "/srv/strata/main/copy"},
		"option injection in host":   {Name: "x", Kind: "rsync", Sources: []string{"/srv/strata/a"}, DestType: "ssh", Host: "-oProxyCommand=id", User: "u", DestPath: "/b"},
		"option injection in user":   {Name: "x", Kind: "rsync", Sources: []string{"/srv/strata/a"}, DestType: "ssh", Host: "h", User: "-x", DestPath: "/b"},
		"relative remote path":       {Name: "x", Kind: "rsync", Sources: []string{"/srv/strata/a"}, DestType: "ssh", Host: "h", User: "u", DestPath: "b"},
		"short restic password":      {Name: "x", Kind: "restic", Sources: []string{"/srv/strata/a"}, DestType: "local", DestPath: "/srv/strata/r", Password: "123"},
		"zfs dest in source":         {Name: "x", Kind: "zfs", Sources: []string{"tank"}, DestType: "local", DestPath: "tank/copy"},
		"zfs dest with shell chars":  {Name: "x", Kind: "zfs", Sources: []string{"tank/a"}, DestType: "ssh", Host: "h", User: "u", DestPath: "pool/x;id"},
		"exclude starting with dash": {Name: "x", Kind: "rsync", Sources: []string{"/srv/strata/a"}, DestType: "local", DestPath: "/srv/strata/b", Excludes: []string{"--delete"}},
		"s3 endpoint not http":       {Name: "x", Kind: "restic", Sources: []string{"/srv/strata/a"}, DestType: "s3", S3Endpoint: "file:///etc", DestPath: "b", AccessKey: "k", SecretKey: "s", Password: "longenough"},
	}
	for name, b := range bad {
		if err := Validate(&b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	r := store.BackupTask{Name: "r", Kind: "restic", Sources: []string{"/srv/strata/a"}, DestType: "local", DestPath: "/srv/strata/r", Password: "longenough"}
	_ = Validate(&r)
	if r.KeepDaily != 7 || r.KeepMonthly != 12 {
		t.Errorf("default restic retention not applied: %+v", r)
	}
}

func TestLineWriter(t *testing.T) {
	var lines []string
	w := &lineWriter{fn: func(l string) { lines = append(lines, l) }}
	_, _ = w.Write([]byte("a\r  10%  \rb\nc"))
	_, _ = w.Write([]byte("d\n"))
	if strings.Join(lines, "|") != "a|  10%  |b|cd" {
		t.Errorf("got %q", lines)
	}
}
