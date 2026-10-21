package iscsi

import (
	"slices"
	"strings"
	"testing"

	"strataos/internal/store"
)

func TestValidIQN(t *testing.T) {
	for _, c := range []struct {
		in string
		ok bool
	}{
		{"iqn.2026-01.lan.home.nas:vm-disks", true},
		{"iqn.1993-08.org.debian:01:a1b2c3d4e5f6", true},
		{"iqn.1991-05.com.microsoft:desktop-ab12.corp.example.com", true},
		{"iqn.1998-01.com.vmware:esx01-5d3f6a1b", true},
		{"iqn.2026-01.strata-vm", true},
		{"eui.02004567a425678d", true},
		{"naa.52004567ba64678d", true},
		{"naa.60014059ddb4c26b3a1d4cbd8f0a7c11", true},
		{"IQN.2026-01.lan.nas:x", false}, // must be normalised first
		{"iqn.2026-13.lan.nas:x", false},
		{"iqn.26-01.lan.nas:x", false},
		{"iqn.2026-01.-lan.nas:x", false},
		{"iqn.2026-01.lan.nas:", false},
		{"iqn.2026-01.lan.nas:a b", false},
		{"iqn.2026-01.lan.nas:a/b", false},
		{"iqn.2026-01.lan_x.nas:a", false},
		{"eui.02004567a425678", false},
		{"naa.xyz", false},
		{"", false},
		{"iqn.2026-01.a:" + strings.Repeat("x", 220), false},
	} {
		if err := ValidIQN(c.in); (err == nil) != c.ok {
			t.Errorf("ValidIQN(%q) = %v, want ok=%v", c.in, err, c.ok)
		}
	}
}

func TestGenerateIQN(t *testing.T) {
	for _, c := range []struct{ host, want string }{
		{"nas.home.lan", "iqn.2026-01.lan.home.nas:disks"},
		{"Strata-VM", "iqn.2026-01.strata-vm:disks"},
		{"my_nas.example.org.", "iqn.2026-01.org.example.mynas:disks"},
		{"", "iqn.2026-01.strataos:disks"},
	} {
		got := GenerateIQN(c.host, "disks")
		if got != c.want {
			t.Errorf("GenerateIQN(%q) = %q, want %q", c.host, got, c.want)
		}
		if err := ValidIQN(got); err != nil {
			t.Errorf("generated IQN invalid: %v", err)
		}
	}
	if s := NewSerial(); !serialRe.MatchString(s) || s == NewSerial() {
		t.Errorf("bad serial %q", s)
	}
}

func TestValidCHAP(t *testing.T) {
	for _, c := range []struct {
		c  store.ISCSICHAP
		ok bool
	}{
		{store.ISCSICHAP{}, true},
		{store.ISCSICHAP{User: "esx", Secret: "Secret-12345"}, true},
		{store.ISCSICHAP{User: "esx", Secret: "short"}, false},
		{store.ISCSICHAP{User: "esx", Secret: "has a space!"}, false},
		{store.ISCSICHAP{Secret: "Secret-12345"}, false},
		{store.ISCSICHAP{User: "esx", Secret: "Secret-12345", MutualUser: "nas", MutualSecret: "Mutual-123456"}, true},
		{store.ISCSICHAP{User: "esx", Secret: "Secret-12345", MutualUser: "nas", MutualSecret: "Secret-12345"}, false},
		{store.ISCSICHAP{User: "esx", Secret: "Secret-12345", MutualSecret: "Mutual-123456"}, false},
		{store.ISCSICHAP{User: "a b", Secret: "Secret-12345"}, false},
	} {
		if err := ValidCHAP(c.c); (err == nil) != c.ok {
			t.Errorf("ValidCHAP(%+v) = %v, want ok=%v", c.c, err, c.ok)
		}
	}
}

func lun(n int, kind, path string) *store.ISCSILUN {
	return &store.ISCSILUN{LUN: n, Kind: kind, Path: path, Size: 1 << 30, Serial: "0123abcd-4567-89ef-0123-456789abcdef"}
}

func TestValidate(t *testing.T) {
	tg := &store.ISCSITarget{ID: "a", Name: "VMs", IQN: " IQN.2026-01.lan.nas:vms ", PortalIP: "0.0.0.0", Initiators: []string{"iqn.1993-08.org.debian:01:AB", "iqn.1993-08.org.debian:01:ab", ""},
		LUNs: []*store.ISCSILUN{lun(0, "fileio", "/srv/strata/main/iscsi/a.img"), lun(1, "zvol", "tank/vm1"), lun(2, "lvm", "vg0/lv0")}}
	tg.LUNs[1].WriteCache = true
	if err := Validate(tg, nil); err != nil {
		t.Fatal(err)
	}
	if tg.Name != "vms" || tg.IQN != "iqn.2026-01.lan.nas:vms" || tg.Port != 3260 || tg.PortalIP != "" || !slices.Equal(tg.Initiators, []string{"iqn.1993-08.org.debian:01:ab"}) {
		t.Errorf("not normalised: %+v", tg)
	}
	if tg.LUNs[1].WriteCache {
		t.Error("write cache kept on a block backstore")
	}
	if Portal(*tg) != "0.0.0.0:3260" || Portal(store.ISCSITarget{PortalIP: "fd00::1", Port: 3261}) != "[fd00::1]:3261" {
		t.Error("bad portal")
	}

	bad := func(name string, mod func(x *store.ISCSITarget), others ...*store.ISCSITarget) {
		x := cloneT(tg)
		mod(x)
		if err := Validate(x, others); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	bad("name", func(x *store.ISCSITarget) { x.Name = "a_b" })
	bad("eui target", func(x *store.ISCSITarget) { x.IQN = "eui.02004567a425678d" })
	bad("initiator", func(x *store.ISCSITarget) { x.Initiators = []string{"windows-pc"} })
	bad("port", func(x *store.ISCSITarget) { x.Port = 7070 })
	bad("portal ip", func(x *store.ISCSITarget) { x.PortalIP = "nas.lan" })
	bad("dup lun", func(x *store.ISCSITarget) { x.LUNs[1].LUN = 0 })
	bad("dup backing", func(x *store.ISCSITarget) { x.LUNs[1].Kind, x.LUNs[1].Path = "fileio", x.LUNs[0].Path })
	bad("outside volumes", func(x *store.ISCSITarget) { x.LUNs[0].Path = "/etc/shadow" })
	bad("volume root", func(x *store.ISCSITarget) { x.LUNs[0].Path = "/srv/strata/main" })
	bad("traversal", func(x *store.ISCSITarget) { x.LUNs[0].Path = "/srv/strata/main/../../../etc/x.img" })
	bad("comma", func(x *store.ISCSITarget) { x.LUNs[0].Path = "/srv/strata/main/a,fd_dev_name=/etc/x" })
	bad("size", func(x *store.ISCSITarget) { x.LUNs[0].Size = 1000 })
	bad("lv", func(x *store.ISCSITarget) { x.LUNs[2].Path = "vg0/../lv" })
	bad("zvol", func(x *store.ISCSITarget) { x.LUNs[1].Path = "tank" })
	bad("zvol snapshot", func(x *store.ISCSITarget) { x.LUNs[1].Path = "tank/vm1@snap" })
	bad("kind", func(x *store.ISCSITarget) { x.LUNs[1].Kind = "rbd" })
	bad("serial", func(x *store.ISCSITarget) { x.LUNs[1].Serial = "x" })
	bad("mixed read-only without ACL", func(x *store.ISCSITarget) { x.AllowAny = true; x.LUNs[0].ReadOnly = true })
	bad("chap", func(x *store.ISCSITarget) { x.CHAP = store.ISCSICHAP{User: "u", Secret: "123"} })
	bad("same name", func(x *store.ISCSITarget) {}, &store.ISCSITarget{ID: "b", Name: "vms", IQN: "iqn.2026-01.lan.nas:other"})
	bad("same iqn", func(x *store.ISCSITarget) {}, &store.ISCSITarget{ID: "b", Name: "other", IQN: "iqn.2026-01.lan.nas:vms"})
	bad("wildcard and address on one port", func(x *store.ISCSITarget) {}, &store.ISCSITarget{ID: "b", Name: "other", IQN: "iqn.2026-01.lan.nas:other", PortalIP: "192.168.1.5", Port: 3260})
	bad("backing exported twice", func(x *store.ISCSITarget) {}, &store.ISCSITarget{ID: "b", Name: "other", IQN: "iqn.2026-01.lan.nas:other", Port: 3260, LUNs: []*store.ISCSILUN{lun(0, "zvol", "tank/vm1")}})

	// Same ID (an edit) does not conflict with itself; all read-only is fine without ACL.
	ok := cloneT(tg)
	ok.AllowAny = true
	for _, l := range ok.LUNs {
		l.ReadOnly = true
	}
	if err := Validate(ok, []*store.ISCSITarget{tg}); err != nil {
		t.Error(err)
	}
}

func cloneT(t *store.ISCSITarget) *store.ISCSITarget {
	c := *t
	c.Initiators = slices.Clone(t.Initiators)
	c.LUNs = nil
	for _, l := range t.LUNs {
		x := *l
		c.LUNs = append(c.LUNs, &x)
	}
	return &c
}

func find(ops []Op, kind, path string) int {
	return slices.IndexFunc(ops, func(o Op) bool { return o.Kind == kind && o.Path == path })
}

func TestPlanACL(t *testing.T) {
	tg := store.ISCSITarget{Name: "vms", IQN: "iqn.2026-01.lan.nas:vms", Port: 3260, Initiators: []string{"iqn.1993-08.org.debian:01:ab"},
		CHAP: store.ISCSICHAP{User: "esx", Secret: "Secret-12345", MutualUser: "nas", MutualSecret: "Mutual-123456"},
		LUNs: []*store.ISCSILUN{lun(0, "fileio", "/srv/strata/main/iscsi/a.img"), lun(1, "zvol", "tank/vm1")}}
	tg.LUNs[0].WriteCache = true
	tg.LUNs[1].ReadOnly = true
	hba := func(l store.ISCSILUN) string {
		if l.Kind == "fileio" {
			return "fileio_0"
		}
		return "iblock_0"
	}
	ops := Plan(tg, hba)
	tpg := "iscsi/iqn.2026-01.lan.nas:vms/tpgt_1"
	acl := tpg + "/acls/iqn.1993-08.org.debian:01:ab"
	want := []Op{
		{Kind: "mkdir", Path: "core/fileio_0/strata_vms_0"},
		{Kind: "write", Path: "core/fileio_0/strata_vms_0/control", Value: "fd_dev_name=/srv/strata/main/iscsi/a.img,fd_dev_size=1073741824"},
		{Kind: "write", Path: "core/fileio_0/strata_vms_0/control", Value: "fd_buffered_io=1"},
		{Kind: "write", Path: "core/iblock_0/strata_vms_1/control", Value: "udev_path=/dev/zvol/tank/vm1"},
		{Kind: "write", Path: "core/iblock_0/strata_vms_1/control", Value: "readonly=1"},
		{Kind: "write", Path: "core/iblock_0/strata_vms_1/wwn/vpd_unit_serial", Value: "0123abcd-4567-89ef-0123-456789abcdef"},
		{Kind: "link", Path: tpg + "/lun/lun_1/strata_vms_1", Value: "core/iblock_0/strata_vms_1"},
		{Kind: "mkdir", Path: tpg + "/np/0.0.0.0:3260"},
		{Kind: "write", Path: tpg + "/attrib/authentication", Value: "1"},
		{Kind: "write", Path: tpg + "/attrib/generate_node_acls", Value: "0"},
		{Kind: "link", Path: acl + "/lun_1/link", Value: tpg + "/lun/lun_1"},
		{Kind: "write", Path: acl + "/lun_1/write_protect", Value: "1"},
		{Kind: "write", Path: acl + "/auth/userid", Value: "esx"},
		{Kind: "write", Path: acl + "/auth/password", Value: "Secret-12345"},
		{Kind: "write", Path: acl + "/auth/userid_mutual", Value: "nas"},
		{Kind: "write", Path: acl + "/auth/password_mutual", Value: "Mutual-123456"},
	}
	for _, w := range want {
		if !slices.Contains(ops, w) {
			t.Errorf("missing %s", w)
		}
	}
	// The LUN 0 is writable through the ACL; nothing goes to the target-wide auth.
	if find(ops, "write", acl+"/lun_0/write_protect") >= 0 || find(ops, "write", tpg+"/auth/userid") >= 0 {
		t.Error("unexpected write protection or target-wide auth")
	}
	// Order: storage enabled before export, link after mkdir, the target enabled last.
	if find(ops, "write", "core/fileio_0/strata_vms_0/enable") > find(ops, "link", tpg+"/lun/lun_0/strata_vms_0") {
		t.Error("storage linked before being enabled")
	}
	if find(ops, "link", acl+"/lun_1/link") > find(ops, "write", acl+"/lun_1/write_protect") {
		t.Error("write protection before the mapped LUN exists")
	}
	if last := ops[len(ops)-1]; last.Path != tpg+"/enable" || last.Value != "1" {
		t.Errorf("last op %s", last)
	}
}

func TestPlanAllowAny(t *testing.T) {
	tg := store.ISCSITarget{Name: "lab", IQN: "iqn.2026-01.lan.nas:lab", Port: 3260, AllowAny: true, Initiators: []string{"iqn.1993-08.org.debian:01:ab"},
		CHAP: store.ISCSICHAP{User: "lab", Secret: "Secret-12345"},
		LUNs: []*store.ISCSILUN{lun(0, "lvm", "vg0/lv0")}}
	tg.LUNs[0].ReadOnly = true
	ops := Plan(tg, func(store.ISCSILUN) string { return "iblock_2" })
	tpg := "iscsi/iqn.2026-01.lan.nas:lab/tpgt_1"
	for _, w := range []Op{
		{Kind: "write", Path: tpg + "/attrib/generate_node_acls", Value: "1"},
		{Kind: "write", Path: tpg + "/attrib/demo_mode_write_protect", Value: "1"},
		{Kind: "write", Path: tpg + "/auth/userid", Value: "lab"},
		{Kind: "write", Path: "core/iblock_2/strata_lab_0/control", Value: "udev_path=/dev/vg0/lv0"},
	} {
		if !slices.Contains(ops, w) {
			t.Errorf("missing %s", w)
		}
	}
	for _, o := range ops {
		if strings.Contains(o.Path, "/acls/") {
			t.Errorf("ACL created although every initiator is allowed: %s", o)
		}
	}
	// No CHAP: authentication off, no credentials written.
	tg.CHAP = store.ISCSICHAP{}
	ops = Plan(tg, func(store.ISCSILUN) string { return "iblock_2" })
	if !slices.Contains(ops, Op{Kind: "write", Path: tpg + "/attrib/authentication", Value: "0"}) || find(ops, "write", tpg+"/auth/userid") >= 0 {
		t.Error("CHAP not disabled")
	}
}

func TestParseACLInfo(t *testing.T) {
	info := `InitiatorName: iqn.1993-08.org.debian:01:ab
InitiatorAlias: vm
LIO Session ID: 3   ISID: 0x00 0x02 0x3d 0x00 0x00 0x00  TSIH: 3  SessionType: Normal
Session State: TARG_SESS_STATE_LOGGED_IN
---------------------[iSCSI Session Values]-----------------------
  CmdSN/WR  :  CmdSN/WC  :  ExpCmdSN  :  MaxCmdSN  :     ITT    :     TTT
 0x00000045   0x00000045   0x00000045   0x000000c4   0x00000001   0xffffffff
----------------------[iSCSI Connections]-------------------------
CID: 0  Connection State: TARG_CONN_STATE_LOGGED_IN
   Address 10.0.2.15 TCP  StatSN: 0x00000040
`
	got := parseACLInfo(info)
	if len(got) != 1 || got[0].Initiator != "iqn.1993-08.org.debian:01:ab" || got[0].Address != "10.0.2.15" {
		t.Errorf("got %+v", got)
	}
	if got := parseACLInfo("No active iSCSI Session for Initiator Endpoint: iqn.1993-08.org.debian:01:ab\n"); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}
