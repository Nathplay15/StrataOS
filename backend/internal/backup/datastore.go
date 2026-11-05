package backup

// Datastores: restic repositories shared by several backup tasks (and
// several NAS). restic gives the properties a backup server needs:
// content-defined chunking, so identical blocks of different files, disks
// or NAS are stored once; encryption; and a checker that can read the data
// back in subsets. This file holds the repository-wide operations: sizes,
// integrity checks, pruning and the list of what it holds.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"strataos/internal/store"
)

// ApplyDatastore gives a task the repository of its datastore.
func ApplyDatastore(t *store.BackupTask, ds *store.Datastore) {
	t.DestType, t.DestPath, t.Host, t.User, t.Port = ds.DestType, ds.DestPath, ds.Host, ds.User, ds.Port
	t.S3Endpoint, t.AccessKey, t.SecretKey, t.Password = ds.S3Endpoint, ds.AccessKey, ds.SecretKey, ds.Password
	t.VaultURL, t.VaultToken, t.VaultCert, t.AppendOnly = ds.VaultURL, ds.VaultToken, ds.VaultCert, ds.AppendOnly
	t.Datastore = ds.ID
}

// RepoTask is a task that only names a datastore's repository (for the
// commands that concern the whole repository).
func RepoTask(ds *store.Datastore) store.BackupTask {
	t := store.BackupTask{ID: "ds-" + ds.ID, Name: ds.Name, Kind: "restic"}
	ApplyDatastore(&t, ds)
	return t
}

// ValidateDatastore checks a datastore before it is saved.
func ValidateDatastore(ds *store.Datastore) error {
	ds.Name = strings.TrimSpace(ds.Name)
	if ds.Name == "" || len(ds.Name) > 60 {
		return fmt.Errorf("invalid name")
	}
	t := RepoTask(ds)
	if t.DestType == "sftp" {
		if !hostRe.MatchString(t.Host) {
			return fmt.Errorf("invalid host")
		}
		if !userRe.MatchString(t.User) {
			return fmt.Errorf("invalid SSH user")
		}
		if t.Port < 0 || t.Port > 65535 {
			return fmt.Errorf("invalid port")
		}
	}
	if strings.ContainsAny(t.DestPath, "\n\r") || strings.HasPrefix(t.DestPath, "-") {
		return fmt.Errorf("invalid destination")
	}
	if err := validateResticDest(&t); err != nil {
		return err
	}
	ds.DestPath = t.DestPath
	if ds.Verify.Subsets < 0 || ds.Verify.Subsets > 100 {
		return fmt.Errorf("the data is read over 1 to 100 checks")
	}
	return nil
}

// TestDatastore reaches the repository, creating it when it is new.
func TestDatastore(ctx context.Context, ds *store.Datastore) error {
	t := RepoTask(ds)
	return ensureRepo(ctx, &t, Progress{Set: func(float64) {}, Log: func(string, ...any) {}})
}

// forgetArgs are the retention options of a task (fixed order).
func forgetArgs(t *store.BackupTask) []string {
	args := []string{"--tag", "strata-" + t.ID, "--group-by", "host,tags"}
	for _, k := range []struct {
		flag string
		n    int
	}{{"--keep-last", t.KeepLast}, {"--keep-hourly", t.KeepHourly}, {"--keep-daily", t.KeepDaily},
		{"--keep-weekly", t.KeepWeekly}, {"--keep-monthly", t.KeepMonthly}, {"--keep-yearly", t.KeepYearly}} {
		if k.n > 0 {
			args = append(args, k.flag, strconv.Itoa(k.n))
		}
	}
	return args
}

// ---- Sizes ----

// ParseStats reads the outputs of "restic stats --json" in raw-data and
// restore-size modes.
func ParseStats(raw, restore []byte) (store.DatastoreStats, error) {
	var r struct {
		TotalSize             int64 `json:"total_size"`
		TotalUncompressedSize int64 `json:"total_uncompressed_size"`
		Snapshots             int   `json:"snapshots_count"`
	}
	var l struct {
		TotalSize int64 `json:"total_size"`
	}
	if err := json.Unmarshal(lastJSON(raw), &r); err != nil {
		return store.DatastoreStats{}, fmt.Errorf("cannot read the repository statistics: %v", err)
	}
	if err := json.Unmarshal(lastJSON(restore), &l); err != nil {
		return store.DatastoreStats{}, fmt.Errorf("cannot read the repository statistics: %v", err)
	}
	st := store.DatastoreStats{Snapshots: r.Snapshots, Logical: l.TotalSize, Stored: r.TotalSize, Unique: r.TotalUncompressedSize}
	if st.Unique == 0 {
		st.Unique = st.Stored // repository format 1: no compression
	}
	if st.Unique > 0 {
		st.Dedup = float64(st.Logical) / float64(st.Unique)
	}
	if st.Stored > 0 {
		st.Ratio = float64(st.Logical) / float64(st.Stored)
	}
	return st, nil
}

// lastJSON is the last line of an output that looks like a JSON object
// (restic may print notices before it).
func lastJSON(b []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if l := bytes.TrimSpace(lines[i]); bytes.HasPrefix(l, []byte("{")) {
			return l
		}
	}
	return b
}

// Stats measures a datastore.
func Stats(ctx context.Context, ds *store.Datastore) (store.DatastoreStats, error) {
	t := RepoTask(ds)
	lock := []string{"--no-lock"}
	raw, err := restic(ctx, &t, nil, append(lock, "stats", "--json", "--mode", "raw-data")...)
	if err != nil {
		return store.DatastoreStats{}, err
	}
	logical, err := restic(ctx, &t, nil, append(lock, "stats", "--json", "--mode", "restore-size")...)
	if err != nil {
		return store.DatastoreStats{}, err
	}
	st, err := ParseStats([]byte(raw), []byte(logical))
	st.Updated = time.Now()
	return st, err
}

// ---- Integrity checks ----

// VerifySubset is the data subset a check reads ("" with n <= 1: all of
// it) and the next value of the rotation, so that every pack file is read
// once every n checks.
func VerifySubset(next, n int) (string, int) {
	if n <= 1 {
		return "all", 1
	}
	if next < 1 || next > n {
		next = 1
	}
	return fmt.Sprintf("%d/%d", next, n), next%n + 1
}

// checkNoise are restic's advice paragraphs, not errors of their own.
var checkNoise = []string{"The repository contains damaged pack files", "restic repair ", "Damaged pack files can be caused", "Fatal: repository contains errors"}

// ParseCheck reads "restic check --json": the errors (first 30), the
// damaged pack files and whether the summary was reached.
func ParseCheck(out string) (errs, broken []string, numErrors int, done bool) {
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "{") {
			continue
		}
		var m struct {
			Type    string   `json:"message_type"`
			Message string   `json:"message"`
			Num     int      `json:"num_errors"`
			Broken  []string `json:"broken_packs"`
		}
		if json.Unmarshal([]byte(l), &m) != nil {
			continue
		}
		switch m.Type {
		case "summary":
			done, numErrors, broken = true, m.Num, m.Broken
		case "error", "exit_error":
			msg := strings.TrimSpace(m.Message)
			if msg == "" || slices.ContainsFunc(checkNoise, func(n string) bool { return strings.HasPrefix(msg, n) }) {
				continue
			}
			if len(errs) < 30 {
				errs = append(errs, msg)
			}
		}
	}
	return errs, broken, numErrors, done
}

var packIDRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseFindSnapshots reads "restic find --json --pack": the snapshots
// holding blobs of the packs, in order of appearance.
func ParseFindSnapshots(out []byte) []string {
	var ids []string
	add := func(id string) {
		if snapIDRe.MatchString(id) && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			break
		}
		var walk func(any)
		walk = func(v any) {
			switch x := v.(type) {
			case []any:
				for _, e := range x {
					walk(e)
				}
			case map[string]any:
				if s, ok := x["snapshot"].(string); ok {
					add(s)
				}
				if m, ok := x["matches"]; ok {
					walk(m)
				}
			}
		}
		walk(v)
	}
	return ids
}

// Verify checks a datastore: its structure, and the data of subset ("2/4",
// "all" or "" for none). It never writes to the repository but its locks.
func Verify(ctx context.Context, ds *store.Datastore, subset string, p Progress) store.VerifyRun {
	start := time.Now()
	run := store.VerifyRun{Started: start, Subset: subset, Status: "failed"}
	t := RepoTask(ds)
	args := []string{"check", "--json", "--retry-lock", "1h"}
	switch {
	case subset == "all":
		args = append(args, "--read-data")
	case subset != "":
		if !regexp.MustCompile(`^\d{1,3}/\d{1,3}$`).MatchString(subset) {
			run.Message = "invalid subset"
			return run
		}
		args = append(args, "--read-data-subset="+subset)
	}
	p.Log("Checking the repository%s", map[bool]string{true: " (data subset " + subset + ")", false: ""}[subset != ""])
	var out bytes.Buffer
	_, err := restic(ctx, &t, &lineWriter{fn: func(l string) {
		out.WriteString(l + "\n")
	}}, args...)
	errs, broken, num, done := ParseCheck(out.String())
	run.Errors, run.Duration = errs, time.Since(start).Seconds()
	for _, b := range broken {
		if packIDRe.MatchString(b) {
			run.Broken = append(run.Broken, b)
		}
	}
	switch {
	case err == nil && done && num == 0:
		run.Status = "success"
		run.Message = "No errors found"
	case done:
		run.Message = fmt.Sprintf("%d error(s) found, %d damaged pack file(s)", max(num, len(errs)), len(run.Broken))
	case err != nil:
		run.Message = "Check failed: " + err.Error()
		if len(run.Message) > 600 {
			run.Message = run.Message[:600]
		}
	default:
		run.Message = "Check failed"
	}
	if len(run.Broken) > 0 {
		// Which snapshots hold data of the damaged packs (from the index,
		// which is still readable).
		fargs := append([]string{"--no-lock", "find", "--json", "--pack"}, run.Broken[:min(len(run.Broken), 50)]...)
		if fout, ferr := restic(ctx, &t, nil, fargs...); ferr == nil {
			run.Affected = ParseFindSnapshots([]byte(fout))
		} else {
			p.Log("Could not list the snapshots concerned: %v", ferr)
		}
	}
	for _, e := range run.Errors {
		p.Log("%s", e)
	}
	p.Log("%s", run.Message)
	run.Duration = time.Since(start).Seconds()
	return run
}

// Prune removes the data no snapshot uses any more.
func Prune(ctx context.Context, ds *store.Datastore, p Progress) (string, error) {
	if ds.AppendOnly {
		return "", fmt.Errorf("an append-only vault cannot be pruned")
	}
	t := RepoTask(ds)
	start := time.Now()
	_, err := restic(ctx, &t, &lineWriter{fn: func(l string) {
		if strings.TrimSpace(l) != "" {
			p.Log("%s", l)
		}
	}}, "prune", "--retry-lock", "1h")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Unused data removed in %s", time.Since(start).Round(time.Second)), nil
}

// ---- Contents ----

// Item is one backup in a datastore: the folders of a task run, or one
// VM or container of a run (several snapshots: disks and definition).
type Item struct {
	Kind     string    `json:"kind"` // folders | vm | ct
	Host     string    `json:"host"`
	Task     string    `json:"task"` // task id (from the strata-<id> tag)
	Name     string    `json:"name"` // guest name, or the folders
	Time     time.Time `json:"time"`
	ID       string    `json:"id"` // snapshot to restore from (definition or archive)
	Parts    []Part    `json:"parts"`
	Logical  int64     `json:"logical"` // size of what was backed up
	Added    int64     `json:"added"`   // new data stored by this backup
	Complete bool      `json:"complete"`
}

// Part is one snapshot of an item.
type Part struct {
	Name    string `json:"name"` // config | disk-vda | rootfs | folders
	ID      string `json:"id"`
	Logical int64  `json:"logical"`
	Added   int64  `json:"added"`
}

// Tags of image backups: guest (sg-vm-<name>, sg-ct-<name>) and part.
const (
	guestTag = "sg-"
	partTag  = "sp-"
)

func tagValue(tags []string, prefix string) string {
	for _, t := range tags {
		if v, ok := strings.CutPrefix(t, prefix); ok {
			return v
		}
	}
	return ""
}

// isImagePart tells the snapshots of image backups from folder snapshots.
func isImagePart(s ResticSnapshot) bool { return tagValue(s.Tags, partTag) != "" }

// GroupSnapshots turns the snapshots of a repository into backups, newest first.
func GroupSnapshots(snaps []ResticSnapshot) []Item {
	type key struct {
		host, task, guest string
		t                 int64
	}
	byKey := map[key]*Item{}
	var order []key
	for _, s := range snaps {
		task := tagValue(s.Tags, "strata-")
		guest := tagValue(s.Tags, guestTag)
		part := tagValue(s.Tags, partTag)
		k := key{s.Hostname, task, guest, s.Time.Unix()}
		if part == "" {
			k.guest, k.t = "", s.Time.UnixNano() // every folder snapshot is its own backup
			part = "folders"
		}
		it := byKey[k]
		if it == nil {
			it = &Item{Kind: "folders", Host: s.Hostname, Task: task, Time: s.Time, Name: strings.Join(s.Paths, ", ")}
			if kind, name, ok := strings.Cut(guest, "-"); ok && k.guest != "" && (kind == "vm" || kind == "ct") {
				it.Kind, it.Name = kind, name
			}
			byKey[k] = it
			order = append(order, k)
		}
		pt := Part{Name: part, ID: s.ID}
		if s.Summary != nil {
			pt.Logical, pt.Added = s.Summary.TotalBytesProcessed, s.Summary.DataAddedPacked
		}
		it.Parts = append(it.Parts, pt)
		it.Logical += pt.Logical
		it.Added += pt.Added
		switch {
		case part == "folders" || part == "rootfs" && it.Kind == "ct" || part == "config" && it.Kind == "vm":
			it.ID, it.Complete = s.ID, true
		}
	}
	out := make([]Item, 0, len(order))
	for _, k := range order {
		it := byKey[k]
		sort.Slice(it.Parts, func(i, j int) bool { return it.Parts[i].Name < it.Parts[j].Name })
		out = append(out, *it)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// allSnapshots lists every snapshot of a repository (all tasks and NAS).
func allSnapshots(ctx context.Context, t *store.BackupTask, filter ...string) ([]ResticSnapshot, error) {
	out, err := restic(ctx, t, nil, append([]string{"--no-lock", "snapshots", "--json"}, filter...)...)
	if err != nil {
		return nil, err
	}
	var snaps []ResticSnapshot
	if err := json.Unmarshal([]byte(lastJSONArray(out)), &snaps); err != nil {
		return nil, err
	}
	return snaps, nil
}

func lastJSONArray(out string) string {
	if i := strings.Index(out, "["); i >= 0 {
		return out[i:]
	}
	return out
}

// Contents lists the backups of a datastore.
func Contents(ctx context.Context, ds *store.Datastore) ([]Item, error) {
	t := RepoTask(ds)
	snaps, err := allSnapshots(ctx, &t)
	if err != nil {
		return nil, err
	}
	return GroupSnapshots(snaps), nil
}
