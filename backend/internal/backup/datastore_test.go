package backup

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"strataos/internal/store"
)

func TestForgetArgs(t *testing.T) {
	task := &store.BackupTask{ID: "t1", KeepLast: 3, KeepHourly: 24, KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 12, KeepYearly: 2}
	got := strings.Join(forgetArgs(task), " ")
	want := "--tag strata-t1 --group-by host,tags --keep-last 3 --keep-hourly 24 --keep-daily 7 --keep-weekly 4 --keep-monthly 12 --keep-yearly 2"
	if got != want {
		t.Fatalf("forget args:\n got %s\nwant %s", got, want)
	}
	got = strings.Join(forgetArgs(&store.BackupTask{ID: "x", KeepYearly: 5}), " ")
	if got != "--tag strata-x --group-by host,tags --keep-yearly 5" {
		t.Fatalf("only yearly: %s", got)
	}
}

func TestValidateRetentionDefaults(t *testing.T) {
	task := &store.BackupTask{Name: "n", Kind: "restic", DestType: "datastore", Datastore: "abc", VMs: []string{"web"}, Password: "secret123"}
	if err := Validate(task); err != nil {
		t.Fatal(err)
	}
	if task.KeepDaily != 7 || task.KeepWeekly != 4 || task.KeepMonthly != 12 || task.Password != "" {
		t.Fatalf("defaults or secrets: %+v", task)
	}
	task = &store.BackupTask{Name: "n", Kind: "restic", DestType: "datastore", Datastore: "abc", VMs: []string{"web"}, KeepHourly: 6}
	if err := Validate(task); err != nil || task.KeepDaily != 0 {
		t.Fatalf("hourly alone is a retention: %v %+v", err, task)
	}
	if err := Validate(&store.BackupTask{Name: "n", Kind: "restic", DestType: "datastore", Datastore: "../x", VMs: []string{"web"}}); err == nil {
		t.Fatal("invalid datastore id accepted")
	}
}

func TestParseStats(t *testing.T) {
	raw := []byte(`{"total_size":27392861,"total_uncompressed_size":27393012,"compression_ratio":1.0000055,"total_blob_count":16,"snapshots_count":2}`)
	restore := []byte("some notice\n{\"total_size\":41943040,\"total_file_count\":2,\"snapshots_count\":2}\n")
	st, err := ParseStats(raw, restore)
	if err != nil {
		t.Fatal(err)
	}
	if st.Snapshots != 2 || st.Logical != 41943040 || st.Stored != 27392861 || st.Unique != 27393012 {
		t.Fatalf("stats: %+v", st)
	}
	if st.Dedup < 1.53 || st.Dedup > 1.54 || st.Ratio < 1.53 || st.Ratio > 1.54 {
		t.Fatalf("ratios: %+v", st)
	}
	// Repository format 1 has no uncompressed size.
	st, _ = ParseStats([]byte(`{"total_size":100,"snapshots_count":1}`), []byte(`{"total_size":300}`))
	if st.Unique != 100 || st.Dedup != 3 {
		t.Fatalf("v1: %+v", st)
	}
	if _, err := ParseStats([]byte("Fatal"), restore); err == nil {
		t.Fatal("garbage accepted")
	}
}

// Outputs of restic 0.18 "check --json" on a repository with damaged packs.
const checkOut = `{"message_type":"error","message":"pack 2c8169c01d0c3a812a4b62eba5058c5af89d1230d0517bca3a78880ce3fde20a: unexpected file size: got 1007, expected 733\n"}
{"message_type":"error","message":"pack b706f4ab4a114635646a910c49e282730d81a2063b4173e0b8ced47b80d82f66 contains 2 errors: [blob 1f3c: ciphertext verification failed]\n"}
{"message_type":"error","message":"\nThe repository contains damaged pack files. These damaged files must be removed to repair the repository.\n\n"}
{"message_type":"error","message":"restic repair packs 2c8169c01d0c3a812a4b62eba5058c5af89d1230d0517bca3a78880ce3fde20a\nrestic repair snapshots --forget\n\n"}
{"message_type":"summary","num_errors":2,"broken_packs":["2c8169c01d0c3a812a4b62eba5058c5af89d1230d0517bca3a78880ce3fde20a","b706f4ab4a114635646a910c49e282730d81a2063b4173e0b8ced47b80d82f66"],"suggest_repair_index":false,"suggest_prune":false}
{"message_type":"exit_error","code":1,"message":"Fatal: repository contains errors"}`

func TestParseCheck(t *testing.T) {
	errs, broken, n, done := ParseCheck(checkOut)
	if !done || n != 2 || len(broken) != 2 || !strings.HasPrefix(broken[1], "b706f4ab") {
		t.Fatalf("summary: %v %d %v", done, n, broken)
	}
	if len(errs) != 2 || !strings.Contains(errs[0], "unexpected file size") || !strings.Contains(errs[1], "contains 2 errors") {
		t.Fatalf("errors: %q", errs)
	}
	errs, broken, n, done = ParseCheck(`{"message_type":"summary","num_errors":0,"broken_packs":null}`)
	if !done || n != 0 || len(broken) != 0 || len(errs) != 0 {
		t.Fatalf("clean: %v %v %d %v", errs, broken, n, done)
	}
	if _, _, _, done := ParseCheck("Fatal: unable to open repository"); done {
		t.Fatal("no summary means not done")
	}
}

func TestParseFindSnapshots(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	out := `[{"object_type":"blob","id":"1f","path":"/vda.raw","snapshot":"` + a + `"},{"object_type":"blob","id":"2c","snapshot":"` + a + `"},{"object_type":"blob","snapshot":"` + b + `"}]`
	if got := ParseFindSnapshots([]byte(out)); !slices.Equal(got, []string{a, b}) {
		t.Fatalf("snapshots: %v", got)
	}
	// One JSON value per pack, with matches.
	out2 := `{"snapshot":"` + b + `","matches":[{"snapshot":"` + a + `"}]}` + "\n" + `{"snapshot":"zz"}`
	if got := ParseFindSnapshots([]byte(out2)); !slices.Equal(got, []string{b, a}) {
		t.Fatalf("nested: %v", got)
	}
}

func TestVerifySubsetRotation(t *testing.T) {
	// Over n checks, every subset is read once, then it starts again.
	next, seen := 0, []string{}
	for range 5 {
		var s string
		s, next = VerifySubset(next, 4)
		seen = append(seen, s)
	}
	if strings.Join(seen, " ") != "1/4 2/4 3/4 4/4 1/4" {
		t.Fatalf("rotation: %v", seen)
	}
	if s, n := VerifySubset(9, 3); s != "1/3" || n != 2 {
		t.Fatalf("out of range cursor: %s %d", s, n)
	}
	if s, _ := VerifySubset(2, 1); s != "all" {
		t.Fatalf("one subset reads all: %s", s)
	}
}

func snap(id, host string, at time.Time, logical, added int64, tags ...string) ResticSnapshot {
	s := ResticSnapshot{ID: strings.Repeat(id, 64/len(id)), Hostname: host, Time: at, Tags: tags, Paths: []string{"/x"}}
	s.Summary = &struct {
		TotalBytesProcessed int64 `json:"total_bytes_processed"`
		TotalFilesProcessed int   `json:"total_files_processed"`
		DataAddedPacked     int64 `json:"data_added_packed"`
	}{logical, 1, added}
	return s
}

func TestGroupSnapshots(t *testing.T) {
	t1 := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(24 * time.Hour)
	snaps := []ResticSnapshot{
		snap("a1", "nas1", t1, 1<<30, 600<<20, "strata-t1", "sg-vm-web", "sp-disk-vda"),
		snap("a2", "nas1", t1, 10<<30, 1<<20, "strata-t1", "sg-vm-web", "sp-disk-vdb"),
		snap("a3", "nas1", t1, 5000, 4000, "strata-t1", "sg-vm-web", "sp-config"),
		snap("b1", "nas1", t1, 1<<30, 100<<20, "strata-t1", "sg-vm-db", "sp-disk-vda"), // no config: incomplete
		snap("c1", "nas2", t1, 1<<30, 10<<20, "strata-t9", "sg-vm-web", "sp-disk-vda"),
		snap("c2", "nas2", t1, 3000, 2000, "strata-t9", "sg-vm-web", "sp-config"),
		snap("d1", "nas1", t2, 300<<20, 50<<20, "strata-t1", "sg-ct-app", "sp-rootfs"),
		snap("e1", "nas1", t2.Add(time.Minute), 9000, 900, "strata-t1"),
	}
	items := GroupSnapshots(snaps)
	if len(items) != 5 {
		t.Fatalf("items: %+v", items)
	}
	if items[0].Kind != "folders" || items[1].Kind != "ct" || items[1].Name != "app" || !items[1].Complete || items[1].ID != snaps[6].ID {
		t.Fatalf("newest first: %+v %+v", items[0], items[1])
	}
	var web *Item
	for i := range items {
		if items[i].Kind == "vm" && items[i].Name == "web" && items[i].Host == "nas1" {
			web = &items[i]
		}
	}
	if web == nil || len(web.Parts) != 3 || !web.Complete || web.ID != snaps[2].ID || web.Task != "t1" {
		t.Fatalf("web: %+v", web)
	}
	if web.Logical != 11<<30+5000 || web.Added != 601<<20+4000 {
		t.Fatalf("sizes: %d %d", web.Logical, web.Added)
	}
	if web.Parts[0].Name != "config" || web.Parts[1].Name != "disk-vda" {
		t.Fatalf("parts sorted: %+v", web.Parts)
	}
	for _, it := range items {
		if it.Kind == "vm" && it.Name == "db" && it.Complete {
			t.Fatal("a VM without its definition is incomplete")
		}
	}
	if isImagePart(snaps[7]) || !isImagePart(snaps[0]) {
		t.Fatal("image parts")
	}
}

func TestResticSnapshotJSON(t *testing.T) {
	// The summary restic 0.17+ stores in each snapshot.
	in := `[{"time":"2026-11-05T10:00:00+01:00","paths":["/vda.raw"],"hostname":"nas","tags":["strata-t1","sg-vm-a","sp-disk-vda"],"summary":{"data_added_packed":6419727,"total_files_processed":1,"total_bytes_processed":20971520},"id":"00875f01974eb8b80a82c15e4ce2f1158bd2f67f99c05302c7264495c19e7f2f","short_id":"00875f01"}]`
	var s []ResticSnapshot
	if err := json.Unmarshal([]byte(lastJSONArray("notice\n"+in)), &s); err != nil {
		t.Fatal(err)
	}
	it := GroupSnapshots(s)
	if len(it) != 1 || it[0].Kind != "vm" || it[0].Name != "a" || it[0].Logical != 20971520 || it[0].Added != 6419727 || it[0].Complete {
		t.Fatalf("%+v", it)
	}
}
