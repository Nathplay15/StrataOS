package ceph

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos/internal/storage"
	"strataos/internal/sys"
)

func TestConfig(t *testing.T) {
	p := ConfParams{FSID: "3f1c2b4a-0d6e-4a1b-9c7d-2e5f6a7b8c9d", PublicNet: "10.98.0.0/24", ClusterNet: "10.97.0.0/24", Size: 3, MinSize: 2,
		Mons: []Mon{{"ceph1", "10.98.0.1"}, {"ceph2", "10.98.0.2"}, {"ceph3", "10.98.0.3"}}}
	c := Config(p)
	for _, want := range []string{
		"fsid = 3f1c2b4a-0d6e-4a1b-9c7d-2e5f6a7b8c9d\n",
		"mon_initial_members = ceph1, ceph2, ceph3\n",
		"mon_host = [v2:10.98.0.1:3300/0,v1:10.98.0.1:6789/0] [v2:10.98.0.2:3300/0,v1:10.98.0.2:6789/0] [v2:10.98.0.3:3300/0,v1:10.98.0.3:6789/0]\n",
		"public_network = 10.98.0.0/24\n", "cluster_network = 10.97.0.0/24\n",
		"auth_client_required = cephx\n", "auth_allow_insecure_global_id_reclaim = false\n",
		"osd_pool_default_size = 3\n", "osd_pool_default_min_size = 2\n", "osd_pool_default_pg_autoscale_mode = on\n",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in\n%s", want, c)
		}
	}
	for _, bad := range []string{"osd_crush_chooseleaf_type", "mon_allow_pool_size_one", "osd_memory_target", "[osd]"} {
		if strings.Contains(c, bad) {
			t.Errorf("unexpected %q", bad)
		}
	}
	// Same networks: no cluster_network line. Test cluster on one NAS with small memory.
	p.ClusterNet, p.Size, p.MinSize, p.SingleHost, p.SmallMemory = p.PublicNet, 1, 0, true, true
	p.Mons = []Mon{{"solo", "fd00::5"}}
	c = Config(p)
	for _, want := range []string{"mon_host = [v2:[fd00::5]:3300/0,v1:[fd00::5]:6789/0]\n", "osd_pool_default_size = 1\n", "osd_pool_default_min_size = 1\n",
		"mon_allow_pool_size_one = true\n", "osd_crush_chooseleaf_type = 0\n", "[osd]\nosd_memory_target = 939524096\n", "[mds]\nmds_cache_memory_limit"} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in\n%s", want, c)
		}
	}
	if strings.Contains(c, "cluster_network") {
		t.Error("cluster_network written although it is the public network")
	}
}

func TestDefaultMinSize(t *testing.T) {
	for size, want := range map[int]int{1: 1, 2: 1, 3: 2, 4: 2, 5: 3} {
		if got := DefaultMinSize(size); got != want {
			t.Errorf("DefaultMinSize(%d) = %d, want %d", size, got, want)
		}
	}
}

func TestKeys(t *testing.T) {
	k := encodeKey(time.Unix(1700000000, 5), make([]byte, 16))
	raw, _ := base64.StdEncoding.DecodeString(k)
	if len(raw) != 28 || raw[0] != 1 || raw[10] != 16 || !strings.HasPrefix(k, "AQ") || len(k) != 40 {
		t.Fatalf("bad key encoding %q", k)
	}
	if err := ValidKey(NewKey()); err != nil {
		t.Fatal(err)
	}
	// A real key made by ceph-authtool.
	if err := ValidKey("AQBSdFRiAAAAABAAhYqOzKwRFT1CRGlWbTOKEQ=="); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "secret", "AQBSdFRiAAAAABAAhYqOzKwRFT1CRGlWbTOKEQ", "QQBSdFRiAAAAABAAhYqOzKwRFT1CRGlWbTOKEQ==", "AQBSdFRiAAAAABAAhYqOzKwRFT1CRGlWbTOK\nEQ=="} {
		if ValidKey(bad) == nil {
			t.Errorf("ValidKey(%q) accepted", bad)
		}
	}
	if NewKey() == NewKey() {
		t.Error("keys are not random")
	}
	kr := Keyring(Entity{Name: "mon.", Key: "AAA", Caps: CapsMon}, Entity{Name: "client.admin", Key: "BBB", Caps: CapsAdmin})
	want := "[mon.]\n\tkey = AAA\n\tcaps mon = \"allow *\"\n[client.admin]\n\tkey = BBB\n\tcaps mds = \"allow *\"\n\tcaps mgr = \"allow *\"\n\tcaps mon = \"allow *\"\n\tcaps osd = \"allow *\"\n"
	if kr != want {
		t.Errorf("keyring:\n%s\nwant:\n%s", kr, want)
	}
	if KeyOf(kr, "client.admin") != "BBB" || KeyOf(kr, "mon.") != "AAA" || KeyOf(kr, "client.x") != "" || KeyOf(kr, "") != "AAA" {
		t.Error("KeyOf")
	}
	// The output of `ceph auth get-or-create`.
	if KeyOf("[mgr.ceph1]\n        key = AQDx==\n", "mgr.ceph1") != "AQDx==" {
		t.Error("KeyOf with spaces")
	}
}

func TestRedactKeys(t *testing.T) {
	in := "Running command: /usr/bin/ceph-authtool /var/lib/ceph/osd/ceph-0/keyring --create-keyring --name osd.0 --add-key AQBSdFRiAAAAABAAhYqOzKwRFT1CRGlWbTOKEQ=="
	out := RedactKeys(in)
	if strings.Contains(out, "AQBSdFRi") || !strings.Contains(out, "--add-key [key]") {
		t.Errorf("not redacted: %s", out)
	}
	if RedactKeys("osd.0 AQ short") != "osd.0 AQ short" {
		t.Error("redacted a non-key")
	}
}

func TestValidators(t *testing.T) {
	mons, err := ParseMons("10.0.0.1, 10.0.0.2:6789\n[fd00::1]:3300 ceph-mon.lan")
	if err != nil || len(mons) != 4 {
		t.Fatalf("ParseMons: %v %v", mons, err)
	}
	if got := MountMonAddr(mons); got != "10.0.0.1:6789/10.0.0.2:6789/[fd00::1]:3300/ceph-mon.lan:6789" {
		t.Errorf("MountMonAddr = %s", got)
	}
	for _, bad := range []string{"", "10.0.0.1:99999", "a b;c$", "http://x", "10.0.0.1,--conf=/x"} {
		if _, err := ParseMons(bad); err == nil {
			t.Errorf("ParseMons(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"rbd", "cephfs.cephfs.data", "vm_disks-2"} {
		if ValidPool(ok) != nil {
			t.Errorf("ValidPool(%q) refused", ok)
		}
	}
	for _, bad := range []string{"", ".mgr", "-x", "a/b", "a b", "../x", strings.Repeat("a", 64)} {
		if ValidPool(bad) == nil {
			t.Errorf("ValidPool(%q) accepted", bad)
		}
	}
	for _, bad := range []string{"", "client admin", "--id", "a/b"} {
		if ValidUser(bad) == nil {
			t.Errorf("ValidUser(%q) accepted", bad)
		}
	}
	for in, want := range map[string]string{"": "/", "/": "/", "/nas": "/nas", "/nas/data/": "/nas/data"} {
		if got, err := CleanSubPath(in); err != nil || got != want {
			t.Errorf("CleanSubPath(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"nas", "/../x", "/a/..", "/a b", "/a/./b", "//"} {
		if _, err := CleanSubPath(bad); err == nil {
			t.Errorf("CleanSubPath(%q) accepted", bad)
		}
	}
	if ValidFSID(NewFSID()) != nil || ValidFSID("x") == nil {
		t.Error("fsid")
	}
}

func TestClientConfigAndSources(t *testing.T) {
	c := ClientConfig("3f1c2b4a-0d6e-4a1b-9c7d-2e5f6a7b8c9d", []string{"10.0.0.1", "10.0.0.2:6789"})
	if !strings.Contains(c, "fsid = 3f1c2b4a-0d6e-4a1b-9c7d-2e5f6a7b8c9d\n") || !strings.Contains(c, "mon_host = 10.0.0.1,10.0.0.2:6789\n") {
		t.Error(c)
	}
	src := CephFSSource("nas2", "3f1c2b4a-0d6e-4a1b-9c7d-2e5f6a7b8c9d", "cephfs", "/shares")
	if src != "nas2@3f1c2b4a-0d6e-4a1b-9c7d-2e5f6a7b8c9d.cephfs=/shares" {
		t.Error(src)
	}
	opts := CephFSOptions("/etc/ceph/strata/ext-1.secret", []string{"10.0.0.1"})
	if opts != "secretfile=/etc/ceph/strata/ext-1.secret,mon_addr=10.0.0.1:6789,noatime,_netdev,noauto,nofail" {
		t.Error(opts)
	}
	caps, err := ClientCaps("rbd", "cephfs")
	if err != nil || strings.Join(caps, "|") != "mon|profile rbd, allow r fsname=cephfs|osd|profile rbd pool=rbd, allow rw tag cephfs data=cephfs|mds|allow rw fsname=cephfs|mgr|profile rbd pool=rbd" {
		t.Errorf("caps %q %v", caps, err)
	}
	if _, err := ClientCaps("", ""); err == nil {
		t.Error("caps without pool nor filesystem")
	}
	x := LibvirtPoolXML("ceph-local-rbd", "rbd", "strata-vms", "6a0c8b7e-0000-4000-8000-000000000001", []string{"10.0.0.1", "[fd00::1]:3300"})
	for _, want := range []string{"<pool type='rbd'>", "<name>ceph-local-rbd</name>", "<name>rbd</name>", "<host name='10.0.0.1'/>", "<host name='fd00::1' port='3300'/>", "<auth type='ceph' username='strata-vms'>", "<secret uuid='6a0c8b7e-0000-4000-8000-000000000001'/>"} {
		if !strings.Contains(x, want) {
			t.Errorf("pool XML lacks %q:\n%s", want, x)
		}
	}
}

const statusJSON = `{"fsid":"3f1c2b4a-0d6e-4a1b-9c7d-2e5f6a7b8c9d","health":{"status":"HEALTH_WARN","checks":{
 "OSD_DOWN":{"severity":"HEALTH_WARN","summary":{"message":"1 osds down","count":1},"muted":false},
 "PG_DEGRADED":{"severity":"HEALTH_WARN","summary":{"message":"Degraded data redundancy: 40/120 objects degraded (33.333%), 30 pgs degraded","count":30},"muted":false},
 "MON_DOWN":{"severity":"HEALTH_WARN","summary":{"message":"1/3 mons down, quorum ceph1,ceph2","count":1},"muted":false}},"mutes":[]},
 "election_epoch":12,"quorum":[0,1],"quorum_names":["ceph1","ceph2"],"quorum_age":42,
 "monmap":{"epoch":1,"min_mon_release_name":"reef","num_mons":3},
 "osdmap":{"epoch":40,"num_osds":3,"num_up_osds":2,"osd_up_since":1700000000,"num_in_osds":3,"osd_in_since":1700000000,"num_remapped_pgs":0},
 "pgmap":{"pgs_by_state":[{"state_name":"active+undersized+degraded","count":30},{"state_name":"active+undersized","count":67}],
  "num_pgs":97,"num_pools":4,"num_objects":40,"data_bytes":1000,"bytes_used":300000000,"bytes_avail":25000000000,"bytes_total":25769803776,
  "degraded_objects":40,"degraded_total":120,"degraded_ratio":0.3333,"read_bytes_sec":1024,"write_bytes_sec":2048,"read_op_per_sec":3,"write_op_per_sec":4},
 "fsmap":{"epoch":9,"id":1,"up":1,"in":1,"max":1,"by_rank":[{"filesystem_id":1,"rank":0,"name":"ceph1","status":"up:active","gid":4242}],"up:standby":1},
 "mgrmap":{"available":true,"num_standbys":1,"modules":["iostat","nfs","restful"],"services":{}},
 "servicemap":{"epoch":1,"modified":"0.000000","services":{}},
 "progress_events":{"9a7c":{"message":"Global Recovery Event (12s)\n      [=======.....................] ","progress":0.25}}}`

func TestParseStatus(t *testing.T) {
	st, err := ParseStatus([]byte(statusJSON))
	if err != nil {
		t.Fatal(err)
	}
	if st.Health.Status != "HEALTH_WARN" || len(st.Health.Checks) != 3 || st.Health.Checks[0].Code != "MON_DOWN" || st.Health.Checks[0].Explain == "" {
		t.Errorf("health %+v", st.Health)
	}
	if strings.Join(st.Quorum, ",") != "ceph1,ceph2" || st.Mons != 3 || st.OSDs != 3 || st.OSDsUp != 2 || st.OSDsIn != 3 || st.PGs != 97 || st.Pools != 4 {
		t.Errorf("counts %+v", st)
	}
	if st.Total != 25769803776 || st.Used != 300000000 || st.ReadBps != 1024 || st.WriteOps != 4 || st.DegradedRatio < 0.33 {
		t.Errorf("usage %+v", st)
	}
	if !st.MgrAvailable || st.MgrStandbys != 1 || len(st.MDS) != 1 || st.MDS[0].Status != "up:active" || st.MDSStandbys != 1 {
		t.Errorf("daemons %+v", st)
	}
	if st.PGState[0].State != "active+undersized" || st.Clean() {
		t.Errorf("pg states %+v", st.PGState)
	}
	if len(st.Progress) != 1 || st.Progress[0].Message != "Global Recovery Event (12s)" || st.Progress[0].Progress != 0.25 {
		t.Errorf("progress %+v", st.Progress)
	}
	ok, _ := ParseStatus([]byte(`{"health":{"status":"HEALTH_OK","checks":{}},"pgmap":{"num_pgs":33,"pgs_by_state":[{"state_name":"active+clean","count":33}]}}`))
	if !ok.Clean() || len(ok.Health.Checks) != 0 {
		t.Error("clean status")
	}
	if _, err := ParseStatus([]byte("not json")); err == nil {
		t.Error("bad JSON accepted")
	}
}

const osdTreeJSON = `{"nodes":[{"id":-1,"name":"default","type":"root","type_id":11,"children":[-7,-5,-3]},
 {"id":-3,"name":"ceph1","type":"host","type_id":1,"pool_weights":{},"children":[0,3]},
 {"id":0,"device_class":"hdd","name":"osd.0","type":"osd","type_id":0,"crush_weight":0.0077972412109375,"depth":2,"pool_weights":{},"exists":1,"status":"up","reweight":1,"primary_affinity":1},
 {"id":3,"device_class":"ssd","name":"osd.3","type":"osd","type_id":0,"crush_weight":0.0077972412109375,"depth":2,"pool_weights":{},"exists":1,"status":"up","reweight":0,"primary_affinity":1},
 {"id":-5,"name":"ceph2","type":"host","type_id":1,"pool_weights":{},"children":[1]},
 {"id":1,"device_class":"hdd","name":"osd.1","type":"osd","type_id":0,"crush_weight":0.0077972412109375,"depth":2,"pool_weights":{},"exists":1,"status":"down","reweight":1,"primary_affinity":1},
 {"id":-7,"name":"ceph3","type":"host","type_id":1,"pool_weights":{},"children":[2]},
 {"id":2,"device_class":"hdd","name":"osd.2","type":"osd","type_id":0,"crush_weight":0.0077972412109375,"depth":2,"pool_weights":{},"exists":1,"status":"destroyed","reweight":0,"primary_affinity":1}],
 "stray":[{"id":4,"name":"osd.4","type":"osd","type_id":0,"crush_weight":0,"depth":0,"exists":1,"status":"down","reweight":0}]}`

func TestParseOSDs(t *testing.T) {
	osds, err := ParseOSDTree([]byte(osdTreeJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(osds) != 5 {
		t.Fatalf("%d OSDs", len(osds))
	}
	o := osds[0]
	if o.ID != 0 || o.Host != "ceph1" || o.Status != "up" || !o.In || o.Class != "hdd" {
		t.Errorf("osd.0 %+v", o)
	}
	if osds[3].ID != 3 || osds[3].In || osds[1].Host != "ceph2" || osds[1].Status != "down" || osds[2].Status != "destroyed" || osds[4].Host != "" {
		t.Errorf("osds %+v", osds)
	}
	df := `{"nodes":[{"id":0,"device_class":"hdd","name":"osd.0","type":"osd","crush_weight":0.0078,"depth":2,"reweight":1,"kb":8384512,"kb_used":1048576,"kb_used_data":1,"kb_avail":7335936,"utilization":12.5,"var":1,"pgs":97,"status":"up"}],"stray":[],"summary":{}}`
	if err := MergeOSDDF(osds, []byte(df)); err != nil || osds[0].Size != 8384512*1024 || osds[0].Utilization != 12.5 || osds[0].PGs != 97 {
		t.Errorf("df %+v %v", osds[0], err)
	}
	meta := `[{"id":0,"hostname":"ceph1","devices":"sdb","bluestore_bdev_dev_node":"/dev/dm-0"},{"id":4,"hostname":"ceph9","devices":""}]`
	if err := MergeOSDMetadata(osds, []byte(meta)); err != nil || osds[0].Device != "/dev/sdb" || osds[4].Host != "ceph9" {
		t.Errorf("metadata %+v %+v %v", osds[0], osds[4], err)
	}
}

func TestParseDaemonsAndPools(t *testing.T) {
	mons, err := ParseMonDump([]byte(`{"epoch":1,"fsid":"x","mons":[{"rank":0,"name":"ceph1","public_addrs":{"addrvec":[]},"addr":"10.98.0.1:6789/0"},{"rank":1,"name":"ceph3","addr":"10.98.0.3:6789/0"}]}`), []string{"ceph1"})
	if err != nil || len(mons) != 2 || mons[0].Status != "up" || mons[0].Addr != "10.98.0.1:6789" || mons[1].Status != "down" {
		t.Errorf("mons %+v %v", mons, err)
	}
	mgrs, err := ParseMgrDump([]byte(`{"epoch":5,"active_gid":1,"active_name":"ceph2","available":true,"standbys":[{"gid":2,"name":"ceph1"},{"gid":3,"name":"ceph3"}]}`))
	if err != nil || len(mgrs) != 3 || mgrs[0].Name != "ceph2" || mgrs[0].Status != "up" || mgrs[2].Status != "standby" {
		t.Errorf("mgrs %+v %v", mgrs, err)
	}
	mds, err := ParseFSDump([]byte(`{"epoch":9,"standbys":[{"gid":5,"name":"ceph3","state":"up:standby"}],"filesystems":[{"mdsmap":{"fs_name":"cephfs","info":{"gid_4242":{"gid":4242,"name":"ceph1","state":"up:active"}}},"id":1}]}`))
	if err != nil || len(mds) != 2 || mds[0].Name != "ceph1" || mds[0].Status != "up" || mds[0].Addr != "cephfs" || mds[1].Status != "standby" {
		t.Errorf("mds %+v %v", mds, err)
	}
	pools, err := ParsePools([]byte(`[{"pool_id":1,"pool_name":".mgr","size":3,"min_size":2,"pg_num":1,"pg_autoscale_mode":"on","application_metadata":{"mgr":{}}},{"pool_id":2,"pool_name":"rbd","size":3,"min_size":2,"pg_num":32,"pg_autoscale_mode":"on","application_metadata":{"rbd":{}}}]`))
	if err != nil || len(pools) != 2 || pools[1].Name != "rbd" || pools[1].Apps[0] != "rbd" || pools[1].PGs != 32 || pools[1].MinSize != 2 {
		t.Fatalf("pools %+v %v", pools, err)
	}
	df := `{"stats":{"total_bytes":1},"pools":[{"name":"rbd","id":2,"stats":{"stored":1000,"objects":7,"kb_used":3,"bytes_used":3000,"percent_used":0.0012,"max_avail":8000000000}}]}`
	if err := MergeDF(pools, []byte(df)); err != nil || pools[1].Stored != 1000 || pools[1].Used != 3000 || pools[1].Objects != 7 || pools[1].MaxAvail != 8000000000 || pools[1].PercentUsed < 0.1 {
		t.Errorf("df %+v %v", pools[1], err)
	}
	fss, err := ParseFSList([]byte(`[{"name":"cephfs","metadata_pool":"cephfs.cephfs.meta","metadata_pool_id":3,"data_pool_ids":[4],"data_pools":["cephfs.cephfs.data"]}]`))
	if err != nil || len(fss) != 1 || fss[0].DataPools[0] != "cephfs.cephfs.data" {
		t.Errorf("fs %+v %v", fss, err)
	}
	if !HasFlag([]byte(`{"flags":"noout,sortbitwise,recovery_deletes"}`), "noout") || HasFlag([]byte(`{"flags":"sortbitwise"}`), "noout") {
		t.Error("flags")
	}
	vl, err := ParseVolumeList([]byte(`{"0":[{"devices":["/dev/sdb"],"lv_name":"osd-block-x","tags":{"ceph.osd_id":"0"},"type":"block","vg_name":"ceph-x"}]}`))
	if err != nil || len(vl[0]) != 1 || vl[0][0] != "/dev/sdb" {
		t.Errorf("volume list %v %v", vl, err)
	}
}

func TestEligibility(t *testing.T) {
	disks := []storage.BlockDevice{
		{Name: "vda", Path: "/dev/vda", Size: 20 << 30, System: true, Usage: "system"},
		{Name: "sda", Path: "/dev/sda", Size: 8 << 30, Usage: "free", Kind: "hdd"},
		{Name: "sdb", Path: "/dev/sdb", Size: 8 << 30, Usage: "filesystem", Kind: "hdd", FSType: "ext4"},
		{Name: "sdc", Path: "/dev/sdc", Size: 8 << 30, Usage: "partitioned", Kind: "hdd", Children: []storage.BlockDevice{{Path: "/dev/sdc1", Mountpoints: []string{"/mnt"}}}},
		{Name: "sdd", Path: "/dev/sdd", Size: 8 << 30, Usage: "raid", Kind: "hdd", Children: []storage.BlockDevice{{Path: "/dev/md0"}}},
		{Name: "sde", Path: "/dev/sde", Size: 8 << 30, Usage: "btrfs", Kind: "hdd"},
		{Name: "sdf", Path: "/dev/sdf", Size: 2 << 30, Usage: "free", Kind: "hdd"},
		{Name: "sdg", Path: "/dev/sdg", Size: 8 << 30, Usage: "ceph", Kind: "hdd"},
		{Name: "sdh", Path: "/dev/sdh", Size: 8 << 30, Usage: "free", Kind: "usb", Removable: true},
		{Name: "sdi", Path: "/dev/sdi", Size: 8 << 30, Usage: "free", Kind: "ssd"},
	}
	used := map[string]string{"/dev/sde": "volume fast", "/dev/md0": "volume main"}
	busy := func(p string) string {
		if p == "/dev/sdi" {
			return "burn-in test"
		}
		return ""
	}
	got := Eligibility(disks, used, busy)
	want := map[string]struct {
		ok, erase bool
		reason    string
	}{
		"vda": {false, false, "System disk"}, "sda": {true, false, ""}, "sdb": {true, true, ""},
		"sdc": {false, false, "Has mounted partitions"}, "sdd": {false, false, "Used by volume main"},
		"sde": {false, false, "Used by volume fast"}, "sdf": {false, false, "Too small (5 GB at least)"},
		"sdg": {false, false, "Already a Ceph disk"}, "sdh": {false, false, "USB and removable disks are not suitable for Ceph"},
		"sdi": {false, false, "Busy: burn-in test"},
	}
	for _, d := range got {
		w := want[d.Name]
		if d.Eligible != w.ok || d.Erase != w.erase || d.Reason != w.reason {
			t.Errorf("%s: got %v/%v %q, want %v/%v %q", d.Name, d.Eligible, d.Erase, d.Reason, w.ok, w.erase, w.reason)
		}
	}
	// A RAID member whose array StrataOS does not use is still refused.
	got = Eligibility([]storage.BlockDevice{{Name: "sdj", Path: "/dev/sdj", Size: 8 << 30, Usage: "raid"}}, nil, nil)
	if got[0].Eligible || !strings.HasPrefix(got[0].Reason, "Member of a RAID array") {
		t.Errorf("raid member %+v", got[0])
	}
}

func TestMappedImages(t *testing.T) {
	root := t.TempDir()
	for id, f := range map[string][3]string{"0": {"rbd", "vol1", "3f1c2b4a-0d6e-4a1b-9c7d-2e5f6a7b8c9d"}, "2": {"vms", "disk", "other"}} {
		d := filepath.Join(root, id)
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "pool"), []byte(f[0]+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(d, "name"), []byte(f[1]+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(d, "cluster_fsid"), []byte(f[2]+"\n"), 0o644)
	}
	_ = os.MkdirAll(filepath.Join(root, "notanid"), 0o755)
	m := mappedIn(root)
	if len(m) != 2 || m[0].Device != "/dev/rbd0" || m[0].Pool != "rbd" || m[0].Image != "vol1" || m[1].FSID != "other" {
		t.Errorf("mapped %+v", m)
	}
}

func TestFriendly(t *testing.T) {
	missing := &sys.CmdError{Cmd: "strata-no-such-tool", Code: -1}
	if err := friendly(missing); err == nil || !strings.Contains(err.Error(), "install the Ceph component") {
		t.Errorf("missing tool: %v", err)
	}
	if err := friendly(&sys.CmdError{Cmd: "sh", Code: -1}); err != ErrUnreachable {
		t.Errorf("killed: %v", err)
	}
	if err := friendly(&sys.CmdError{Cmd: "ceph", Code: 13, Stderr: "error: Permission denied"}); err == nil || !strings.Contains(err.Error(), "refused the user or key") {
		t.Errorf("denied: %v", err)
	}
	if err := friendly(&sys.CmdError{Cmd: "ceph", Code: 1, Stderr: "key AQBSdFRiAAAAABAAhYqOzKwRFT1CRGlWbTOKEQ== bad"}); strings.Contains(err.Error(), "AQBSdFRi") {
		t.Errorf("key in error: %v", err)
	}
}
