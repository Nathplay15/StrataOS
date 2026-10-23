package components

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTable(t *testing.T) {
	ids := map[string]bool{}
	for _, c := range All {
		if ids[c.ID] {
			t.Errorf("duplicate component %s", c.ID)
		}
		ids[c.ID] = true
		if c.Name == "" || len(c.Packages) == 0 {
			t.Errorf("%s: name and packages required", c.ID)
		}
		for _, p := range append(append(append(slices.Clone(c.Files), c.Caches...), c.Data...), c.Repo...) {
			if !strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.Count(p, "/") < 2 {
				t.Errorf("%s: unsafe path %q", c.ID, p)
			}
		}
		for _, u := range c.Units {
			if !strings.HasPrefix(u, "strata-") {
				t.Errorf("%s: %s is not a StrataOS unit", c.ID, u)
			}
		}
	}
	for _, id := range []string{"zfs", "docker", "nut", "vms", "rclone", "ftp", "directory", "antivirus", "ha", "vpn", "photos", "tailscale", "tunnel", "iscsi", "dlna", "nvr", "docpreview"} {
		if Find(id) == nil {
			t.Errorf("component %s missing", id)
		}
	}
}

func TestPackages(t *testing.T) {
	all := func(string) bool { return true }
	prot := Protected("mdadm, rsync (>= 3), openssh-client | ssh-client, nftables")
	cases := []struct {
		id         string
		installed  func(string) bool
		inUse      []string
		remove     []string
		keptReason map[string]string
	}{
		// ffmpeg stays while the recorder (or Photos) still needs it.
		{"photos", func(p string) bool { return p != "intel-media-va-driver" }, []string{"nvr"},
			[]string{"libvips-tools", "libheif-plugin-libde265", "libimage-exiftool-perl", "mesa-va-drivers"},
			map[string]string{"ffmpeg": "Used by Surveillance recorder"}},
		{"nvr", all, []string{"photos", "dlna"}, nil, map[string]string{"ffmpeg": "Used by Media tools (ffmpeg, libvips)"}},
		{"nvr", all, nil, []string{"ffmpeg"}, nil},
		// Packages StrataOS needs are never removed.
		{"rclone", all, nil, []string{"rclone"}, map[string]string{"fuse3": "Needed by StrataOS", "openssh-client": "Needed by StrataOS"}},
		{"ha", all, nil, []string{"keepalived"}, map[string]string{"rsync": "Needed by StrataOS", "python3": "Needed by StrataOS", "openssh-server": "Needed by StrataOS"}},
		{"vpn", all, nil, []string{"wireguard-tools"}, map[string]string{"nftables": "Needed by StrataOS"}},
		{"ftp", all, []string{"ftp"}, []string{"vsftpd"}, nil}, // itself does not count
		{"unknown", all, nil, nil, nil},
	}
	for _, c := range cases {
		rm, kept := Packages(c.id, c.installed, c.inUse, prot)
		if !reflect.DeepEqual(rm, c.remove) {
			t.Errorf("%s: remove %v, want %v", c.id, rm, c.remove)
		}
		got := map[string]string{}
		for _, k := range kept {
			got[k.Package] = k.Reason
		}
		if len(got) != len(c.keptReason) {
			t.Errorf("%s: kept %v, want %v", c.id, got, c.keptReason)
		}
		for p, r := range c.keptReason {
			if got[p] != r {
				t.Errorf("%s: %s kept for %q, want %q", c.id, p, got[p], r)
			}
		}
	}
}

func TestParseDepends(t *testing.T) {
	got := ParseDepends("mdadm, lvm2 (>= 2.03), docker.io | docker-ce, python3:any, samba [amd64],")
	want := []string{"mdadm", "lvm2", "docker.io", "docker-ce", "python3", "samba"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	if ParseDepends("") != nil {
		t.Error("empty field")
	}
}

func TestParseAptSim(t *testing.T) {
	out := `NOTE: This is only a simulation!
Reading package lists...
The following packages will be REMOVED:
  minidlna* libavformat61*
Purg minidlna [1.3.3+dfsg-1.1+b3]
Purg libavformat61:amd64 [7:7.1.1-1]
Remv strataos [0.7.0]
Inst foo (1.0 Debian:13)
`
	if got := ParseAptSim(out); !reflect.DeepEqual(got, []string{"minidlna", "libavformat61", "strataos"}) {
		t.Errorf("got %v", got)
	}
	if got := Without([]string{"a", "b", "c"}, []string{"b"}); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Errorf("without: %v", got)
	}
}

func TestBlockers(t *testing.T) {
	f := Facts{Pools: []string{"tank"}, Stacks: []string{"uptimekuma", "dozzle"}, Containers: []string{"atk"}, VMs: []string{"web"},
		Targets: []string{"lun0"}, HARole: "primary", Directory: "ad", VPNServer: true, VPNDevices: 2, VPNClient: true,
		UPSMode: "usb", DLNA: true, FTP: true, Cameras: []string{"door"}, Syncs: []string{"s3"}, AVSchedules: []string{"nightly"}, Tailscale: true, Tunnel: true}
	cases := []struct {
		id    string
		count int
		force bool // every blocker can be forced
		text  string
	}{
		{"zfs", 1, false, "tank"},
		{"docker", 2, false, "dozzle, uptimekuma"},
		{"vms", 1, false, "web"},
		{"iscsi", 1, false, "lun0"},
		{"ha", 1, false, "paired"},
		{"directory", 1, false, "joined"},
		{"vpn", 2, true, "2 device(s)"},
		{"nut", 1, true, "usb"},
		{"dlna", 1, true, "DLNA"},
		{"ftp", 1, true, "FTP"},
		{"nvr", 1, true, "door"},
		{"rclone", 1, true, "s3"},
		{"antivirus", 1, true, "nightly"},
		{"tailscale", 1, true, "Tailscale"},
		{"tunnel", 1, true, "Tunnel"},
		{"photos", 0, true, ""},
		{"docpreview", 0, true, ""},
	}
	for _, c := range cases {
		b := Blockers(c.id, f)
		if len(b) != c.count {
			t.Errorf("%s: %d blockers %v", c.id, len(b), b)
			continue
		}
		var msgs []string
		for _, x := range b {
			msgs = append(msgs, x.Message)
			if x.Force != c.force {
				t.Errorf("%s: %q force=%v", c.id, x.Message, x.Force)
			}
		}
		if !strings.Contains(strings.Join(msgs, "; "), c.text) {
			t.Errorf("%s: %v lacks %q", c.id, msgs, c.text)
		}
		if c.count > 0 && !Refused(b, false) {
			t.Errorf("%s: not refused without force", c.id)
		}
		if c.count > 0 && Refused(b, true) == c.force {
			t.Errorf("%s: force gives refused=%v", c.id, Refused(b, true))
		}
	}
	if len(Blockers("zfs", Facts{})) != 0 || len(Blockers("nut", Facts{UPSMode: "off"})) != 0 {
		t.Error("blockers without facts")
	}
	if Refused(nil, false) {
		t.Error("nothing refuses")
	}
}

func TestAppData(t *testing.T) {
	shares := map[string][]string{"Photos": {"/srv/strata/main/Photos"}, "Media": {"/srv/strata/main/Media"}}
	others := map[string][]string{"jellyfin": {"/srv/strata/apps/jellyfin/config", "/srv/strata/main/Media"}, "x": {"/srv/strata/apps/shared"}}
	del, kept := AppData("frigate", "frigate", []string{
		"/srv/strata/apps/frigate/config",
		"/srv/strata/apps/frigate/config/sub", // inside the first one
		"/srv/strata/apps/frigate/../frigate/media",
		"/srv/strata/main/nvr/frigate/media",         // in a volume, named after the app
		"/srv/strata/main/Media/frigate",             // inside a shared folder
		"/srv/strata/main",                           // a volume
		"/srv/strata/main/downloads",                 // not named after the app
		"/srv/strata/main/Photos",                    // a shared folder
		"/srv/strata/apps/shared",                    // used by x
		"/srv/strata/apps/jellyfin",                  // holds jellyfin's folder
		"/srv/strata/apps",                           // the root itself
		"/etc/localtime", "/var/run/docker.sock", "", // system paths, ignored
		"relative/path",
	}, others, shares)
	want := []string{"/srv/strata/apps/frigate/config", "/srv/strata/apps/frigate/media", "/srv/strata/main/nvr/frigate/media"}
	if !reflect.DeepEqual(del, want) {
		t.Errorf("deleted %v, want %v", del, want)
	}
	reasons := map[string]string{}
	for _, k := range kept {
		reasons[k.Path] = k.Reason
	}
	wantKept := map[string]string{
		"/srv/strata/main/Media/frigate": "In shared folder Media",
		"/srv/strata/main":               "Not an app folder",
		"/srv/strata/main/downloads":     "Not an app folder",
		"/srv/strata/main/Photos":        "In shared folder Photos",
		"/srv/strata/apps/shared":        "Also used by x",
		"/srv/strata/apps/jellyfin":      "Also used by jellyfin",
		"/srv/strata/apps":               "Not an app folder",
	}
	if !reflect.DeepEqual(reasons, wantKept) {
		t.Errorf("kept %v", reasons)
	}
	// In the "apps" folder of a volume, not the folder itself.
	del, kept = AppData("uptimekuma", "uptime", []string{"/srv/strata/main/apps/uptime-kuma", "/srv/strata/main/apps"}, nil, nil)
	if !reflect.DeepEqual(del, []string{"/srv/strata/main/apps/uptime-kuma"}) || len(kept) != 1 {
		t.Errorf("apps folder: %v %v", del, kept)
	}
	// A custom stack's folder named after the stack.
	del, _ = AppData("", "e2e-un", []string{"/srv/strata/fast/e2e-un/data"}, nil, nil)
	if !reflect.DeepEqual(del, []string{"/srv/strata/fast/e2e-un/data"}) {
		t.Errorf("custom: %v", del)
	}
}
