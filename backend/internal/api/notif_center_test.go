package api

import "testing"

func TestNotifKindOf(t *testing.T) {
	cases := []struct{ subject, msg, kind, action, link string }{
		{"Updates available", "StrataOS 0.10.1 is available: install it from Updates", "update", "update", "/system/updates"},
		{"Container updates available", "2 container(s)", "docker", "update", "/docker/containers"},
		{"Disk getting worse", "x", "storage", "disk", "/storage/disks"},
		{"Failing disk", "x", "storage", "disk", "/storage/disks"},
		{"Task failed", "Backup: usb: failed — no space left", "backup", "backup", "/backup"},
		{"Task failed", "Cloud sync: drive: failed — x", "backup", "sync", "/cloudsync"},
		{"Cloud sync failed", "drive : x", "backup", "sync", "/cloudsync"},
		{"Task failed", "Create volume data: failed — x", "system", "", ""},
		{"Security advisor", "Security score: 41/100", "security", "review", "/system/security"},
		{"Alert", "RAID array md0 (data) is degraded", "storage", "open", "/storage/raid"},
		{"Storage", "ZFS pool tank: scrub finished", "storage", "open", "/storage/zfs"},
		{"Weekly report of nas", "💾 Storage", "report", "", ""},
		{"VPN device expiring soon", "x", "network", "open", "/system/remote"},
		{"Test", "Notifications work", "system", "", ""},
	}
	for _, c := range cases {
		k := notifKindOf(c.subject, c.msg)
		if k.Kind != c.kind || k.Action != c.action || k.Link != c.link {
			t.Errorf("%q / %q: got %+v", c.subject, c.msg, k)
		}
	}
}

func TestNotifView(t *testing.T) {
	ev := []mobileEvent{
		{ID: 3, Subject: "Updates available", Kind: "update"},
		{ID: 4, Subject: "Failing disk"}, // recorded before 0.10: classified on read
		{ID: 5, Subject: "Test", Kind: "system"},
	}
	items, unread := notifView(ev, 6, 3, 0)
	if len(items) != 3 || unread != 2 || items[0].ID != 5 || items[1].Kind != "storage" || items[1].Link != "/storage/disks" {
		t.Fatalf("view: %+v %d", items, unread)
	}
	if items, unread := notifView(ev, 6, 5, 4); len(items) != 1 || unread != 0 {
		t.Fatalf("cleared up to 4: %+v %d", items, unread)
	}
	// Markers beyond the feed (reset by a restore): everything is new.
	if items, unread := notifView(ev, 6, 9, 9); len(items) != 3 || unread != 3 {
		t.Fatalf("reset feed: %+v %d", items, unread)
	}
	if items, _ := notifView(nil, 1, 0, 0); items == nil {
		t.Fatal("an empty list must be [] in JSON")
	}
}
