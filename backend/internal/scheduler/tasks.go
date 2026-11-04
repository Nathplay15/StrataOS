package scheduler

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"strataos/internal/antivirus"
	"strataos/internal/docker"
	"strataos/internal/protect"
	"strataos/internal/storage"
	"strataos/internal/store"
)

// Resolver maps a StrataOS volume ID to its mount point.
type Resolver func(volumeID string) (mountpoint string, ok bool)

const stampLayout = "20060102-150405"

// autoPrefix identifies snapshots created by a given schedule, so retention never touches manual ones.
func autoPrefix(s *store.Schedule) string { return "auto-" + s.ID + "-" }

// Validate checks a schedule definition before it is saved.
func Validate(s *store.Schedule, resolve Resolver) error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if _, err := ParseCron(s.Cron); err != nil {
		return err
	}
	switch s.Type {
	case "snapshot":
		for _, n := range []int{s.KeepCount, s.KeepDays, s.KeepHourly, s.KeepDaily, s.KeepWeekly, s.KeepMonthly} {
			if n < 0 || n > 100000 {
				return fmt.Errorf("retention values must be positive")
			}
		}
		if RetentionOf(s).Empty() {
			return fmt.Errorf("set a retention (number of snapshots or days) to avoid filling the volume")
		}
		if s.LockDays < 0 || s.LockDays > 3650 {
			return fmt.Errorf("invalid lock duration")
		}
		switch s.TargetKind {
		case "zfs":
			if s.Target == "" || strings.ContainsAny(s.Target, "@ ") {
				return fmt.Errorf("choose a ZFS dataset")
			}
		case "btrfs":
			if _, ok := resolve(s.Target); !ok {
				return fmt.Errorf("unknown btrfs volume")
			}
			if strings.Contains(s.Subvolume, "..") {
				return fmt.Errorf("invalid subvolume")
			}
		case "lvm":
			vg, lv, ok := strings.Cut(s.Target, "/")
			if !ok || storage.ValidName(vg) != nil || storage.ValidName(lv) != nil {
				return fmt.Errorf("choose an LVM thin volume")
			}
			if !storage.IsThinLV(vg, lv) {
				return fmt.Errorf("%s is not an LVM thin volume: only thin volumes can be snapshotted on a schedule", s.Target)
			}
			if s.LockDays > 0 {
				return fmt.Errorf("LVM snapshots cannot be locked")
			}
		default:
			return fmt.Errorf("snapshots are supported on ZFS datasets, Btrfs volumes and LVM thin volumes")
		}
	case "scrub":
		switch s.TargetKind {
		case "zfs", "mdadm":
			if s.Target == "" {
				return fmt.Errorf("choose a target")
			}
		case "btrfs":
			if _, ok := resolve(s.Target); !ok {
				return fmt.Errorf("unknown btrfs volume")
			}
		default:
			return fmt.Errorf("scrubs are supported on RAID arrays, ZFS pools and Btrfs volumes")
		}
	case "smart":
		s.TargetKind = "disks"
		if s.Target == "" {
			s.Target = "all"
		}
		if s.SmartTest != "short" && s.SmartTest != "long" {
			return fmt.Errorf("SMART test type must be short or long")
		}
	case "antivirus":
		s.TargetKind = "path"
		t, err := antivirus.ValidTarget(s.Target)
		if err != nil {
			return err
		}
		if s.Target != "all" {
			s.Target = t
		}
		if s.Target == "" {
			s.Target = "all"
		}
		if s.AVAction != "report" {
			s.AVAction = "quarantine"
		}
	case "appupdate":
		s.TargetKind = "stacks"
		if s.Target == "" {
			s.Target = "all"
		}
		if s.Target != "all" {
			if err := docker.ValidStack(s.Target); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unknown task type %q", s.Type)
	}
	return nil
}

// Execute runs one occurrence of a schedule and returns a human readable summary.
func Execute(ctx context.Context, s store.Schedule, resolve Resolver, logf func(string, ...any)) (string, error) {
	switch s.Type {
	case "snapshot":
		if s.TargetKind == "zfs" {
			return zfsSnapshot(&s, logf)
		}
		if s.TargetKind == "lvm" {
			return lvmSnapshot(&s, logf)
		}
		mp, ok := resolve(s.Target)
		if !ok {
			return "", fmt.Errorf("volume %s no longer exists", s.Target)
		}
		return btrfsSnapshot(&s, mp, logf)
	case "scrub":
		return scrub(&s, resolve, logf)
	case "smart":
		return smart(&s, logf)
	case "appupdate":
		return appUpdate(ctx, &s, logf)
	case "antivirus":
		return antivirus.Scan(ctx, s.Target, s.AVAction != "report", logf)
	}
	return "", fmt.Errorf("unknown task type %q", s.Type)
}

// expired selects snapshots to delete. items must be sorted newest first; the newest is always kept.
func expired[T any](items []T, created func(T) time.Time, keepCount, keepDays int, now time.Time) []T {
	var out []T
	for i, it := range items {
		if i == 0 {
			continue
		}
		tooMany := keepCount > 0 && i >= keepCount
		tooOld := keepDays > 0 && now.Sub(created(it)) > time.Duration(keepDays)*24*time.Hour
		if tooMany || tooOld {
			out = append(out, it)
		}
	}
	return out
}

// Retention of the snapshots of a task. Without a calendar rule, Count and
// Days work as before 0.10: a snapshot must satisfy both to stay. With one
// (hourly, daily, weekly or monthly), each rule keeps its snapshots and
// Count/Days are rules like the others (protect.Policy).
type Retention struct {
	Count, Days                    int
	Hourly, Daily, Weekly, Monthly int
}

// RetentionOf reads the retention of a schedule.
func RetentionOf(s *store.Schedule) Retention {
	return Retention{Count: s.KeepCount, Days: s.KeepDays, Hourly: s.KeepHourly, Daily: s.KeepDaily, Weekly: s.KeepWeekly, Monthly: s.KeepMonthly}
}

func (r Retention) calendar() bool { return r.Hourly+r.Daily+r.Weekly+r.Monthly > 0 }

// Empty reports whether nothing limits the number of snapshots.
func (r Retention) Empty() bool { return r.Count <= 0 && r.Days <= 0 && !r.calendar() }

// Expire returns the snapshots to delete. items must be sorted newest first.
func Expire[T any](items []T, created func(T) time.Time, r Retention, now time.Time) []T {
	if !r.calendar() {
		return expired(items, created, r.Count, r.Days, now)
	}
	times := make([]time.Time, len(items))
	for i, it := range items {
		times[i] = created(it)
	}
	p := protect.Policy{Last: r.Count, Days: r.Days, Hourly: r.Hourly, Daily: r.Daily, Weekly: r.Weekly, Monthly: r.Monthly}
	var out []T
	for _, i := range protect.Expired(times, p, now) {
		out = append(out, items[i])
	}
	return out
}

// Snap is a snapshot made by a task.
type Snap struct {
	Name   string    `json:"name"`
	Time   time.Time `json:"time"`
	Keep   bool      `json:"keep"`
	Locked bool      `json:"locked,omitempty"`
}

func zfsSnapshot(s *store.Schedule, logf func(string, ...any)) (string, error) {
	return snapshotZFS(s.Target, autoPrefix(s), s.Recursive, RetentionOf(s), s.LockDays, logf)
}

func btrfsSnapshot(s *store.Schedule, mp string, logf func(string, ...any)) (string, error) {
	return snapshotBtrfs(mp, s.Subvolume, autoPrefix(s), RetentionOf(s), s.LockDays, logf)
}

// SnapshotZFS snapshots dataset as <tag><stamp> and applies retention to the
// snapshots carrying the same tag (others, manual or from other tasks, are never touched).
func SnapshotZFS(dataset, tag string, recursive bool, keepCount, keepDays, lockDays int, logf func(string, ...any)) (string, error) {
	return snapshotZFS(dataset, tag, recursive, Retention{Count: keepCount, Days: keepDays}, lockDays, logf)
}

func zfsTagged(dataset, tag string) ([]storage.ZSnapshot, error) {
	all, err := storage.SnapshotsOf(dataset)
	if err != nil {
		return nil, err
	}
	var mine []storage.ZSnapshot
	for _, sn := range all {
		if strings.HasPrefix(sn.Name, dataset+"@"+tag) {
			mine = append(mine, sn)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Creation.After(mine[j].Creation) })
	return mine, nil
}

func snapshotZFS(dataset, tag string, recursive bool, r Retention, lockDays int, logf func(string, ...any)) (string, error) {
	name := tag + time.Now().Format(stampLayout)
	logf("Creating %s@%s", dataset, name)
	if err := storage.Snapshot(dataset, name, recursive); err != nil {
		return "", err
	}
	if lockDays > 0 {
		if err := storage.LockZFSSnapshot(dataset+"@"+name, time.Now().AddDate(0, 0, lockDays), recursive); err != nil {
			return "", fmt.Errorf("snapshot created but not locked: %w", err)
		}
	}
	mine, err := zfsTagged(dataset, tag)
	if err != nil {
		return "", err
	}
	removed := 0
	for _, sn := range Expire(mine, func(z storage.ZSnapshot) time.Time { return z.Creation }, r, time.Now()) {
		if time.Now().Before(storage.ZFSLockUntil(sn.Name)) {
			continue // still locked: kept until its date
		}
		logf("Retention: destroying %s", sn.Name)
		if err := storage.DestroyDataset(sn.Name, recursive); err != nil {
			logf("warning: %v", err)
			continue
		}
		removed++
	}
	return fmt.Sprintf("Snapshot %s@%s created, %d old one(s) deleted, %d kept", dataset, name, removed, len(mine)-removed), nil
}

// SnapshotBtrfs snapshots the sub-volume sub of the Btrfs volume mp (the whole
// volume when empty) as .snapshots/<sub>@<tag><stamp>, then applies retention
// to the snapshots carrying the same tag.
func SnapshotBtrfs(mp, sub, tag string, keepCount, keepDays, lockDays int, logf func(string, ...any)) (string, error) {
	return snapshotBtrfs(mp, sub, tag, Retention{Count: keepCount, Days: keepDays}, lockDays, logf)
}

func btrfsPrefix(sub, tag string) string {
	base := "root"
	if sub != "" {
		base = strings.ReplaceAll(strings.Trim(sub, "/"), "/", "_")
	}
	return base + "@" + tag
}

// btrfsTagged lists the snapshots of prefix, newest first; their time is
// the stamp of their name.
func btrfsTagged(mp, prefix string) ([]Snap, error) {
	names, err := storage.BtrfsSnapshotNames(mp)
	if err != nil {
		return nil, err
	}
	var mine []Snap
	for _, n := range names {
		if !strings.HasPrefix(n, prefix) {
			continue
		}
		at, err := time.ParseInLocation(stampLayout, strings.TrimPrefix(n, prefix), time.Local)
		if err != nil {
			continue
		}
		mine = append(mine, Snap{Name: n, Time: at})
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Time.After(mine[j].Time) })
	return mine, nil
}

func snapshotBtrfs(mp, sub, tag string, r Retention, lockDays int, logf func(string, ...any)) (string, error) {
	prefix := btrfsPrefix(sub, tag)
	name := prefix + time.Now().Format(stampLayout)
	logf("Creating %s/.snapshots/%s", mp, name)
	if _, err := storage.BtrfsSnapshot(mp, sub, name); err != nil {
		return "", err
	}
	if lockDays > 0 {
		if err := storage.LockBtrfsSnapshot(mp, name, time.Now().AddDate(0, 0, lockDays)); err != nil {
			return "", fmt.Errorf("snapshot created but not locked: %w", err)
		}
	}
	mine, err := btrfsTagged(mp, prefix)
	if err != nil {
		return "", err
	}
	removed := 0
	for _, sn := range Expire(mine, func(x Snap) time.Time { return x.Time }, r, time.Now()) {
		if time.Now().Before(storage.BtrfsLockUntil(mp, sn.Name)) {
			continue // still locked: kept until its date
		}
		logf("Retention: deleting .snapshots/%s", sn.Name)
		if err := storage.BtrfsDeleteSubvol(mp, ".snapshots/"+sn.Name); err != nil {
			logf("warning: %v", err)
			continue
		}
		removed++
	}
	return fmt.Sprintf("Snapshot .snapshots/%s created, %d old one(s) deleted, %d kept", name, removed, len(mine)-removed), nil
}

// lvmSnapshot makes a thin snapshot of an LVM thin volume (vg/lv) named
// <lv>-<tag><stamp>, then applies retention to those of the same tag.
func lvmSnapshot(s *store.Schedule, logf func(string, ...any)) (string, error) {
	vg, lv, _ := strings.Cut(s.Target, "/")
	prefix := lv + "-" + autoPrefix(s)
	name := prefix + time.Now().Format(stampLayout)
	logf("Creating the thin snapshot %s/%s", vg, name)
	if err := storage.ThinSnapshot(vg, lv, name); err != nil {
		return "", err
	}
	mine, err := lvmTagged(vg, lv, prefix)
	if err != nil {
		return "", err
	}
	removed := 0
	for _, sn := range Expire(mine, func(x Snap) time.Time { return x.Time }, RetentionOf(s), time.Now()) {
		logf("Retention: removing %s/%s", vg, sn.Name)
		if err := storage.RemoveThinSnapshot(vg, sn.Name); err != nil {
			logf("warning: %v", err)
			continue
		}
		removed++
	}
	return fmt.Sprintf("Snapshot %s/%s created, %d old one(s) deleted, %d kept", vg, name, removed, len(mine)-removed), nil
}

func lvmTagged(vg, lv, prefix string) ([]Snap, error) {
	names, err := storage.ThinSnapshotNames(vg, lv)
	if err != nil {
		return nil, err
	}
	var mine []Snap
	for _, n := range names {
		if !strings.HasPrefix(n, prefix) {
			continue
		}
		at, err := time.ParseInLocation(stampLayout, strings.TrimPrefix(n, prefix), time.Local)
		if err != nil {
			continue
		}
		mine = append(mine, Snap{Name: n, Time: at})
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Time.After(mine[j].Time) })
	return mine, nil
}

// TaskSnapshots lists the snapshots a snapshot task made, newest first,
// with what its retention keeps (as if it ran now).
func TaskSnapshots(s store.Schedule, resolve Resolver) ([]Snap, error) {
	var mine []Snap
	switch s.TargetKind {
	case "zfs":
		zs, err := zfsTagged(s.Target, autoPrefix(&s))
		if err != nil {
			return nil, err
		}
		for _, z := range zs {
			_, n, _ := strings.Cut(z.Name, "@")
			mine = append(mine, Snap{Name: n, Time: z.Creation, Locked: time.Now().Before(storage.ZFSLockUntil(z.Name))})
		}
	case "btrfs":
		mp, ok := resolve(s.Target)
		if !ok {
			return nil, fmt.Errorf("volume %s no longer exists", s.Target)
		}
		list, err := btrfsTagged(mp, btrfsPrefix(s.Subvolume, autoPrefix(&s)))
		if err != nil {
			return nil, err
		}
		for _, b := range list {
			b.Locked = time.Now().Before(storage.BtrfsLockUntil(mp, b.Name))
			mine = append(mine, b)
		}
	case "lvm":
		vg, lv, _ := strings.Cut(s.Target, "/")
		list, err := lvmTagged(vg, lv, lv+"-"+autoPrefix(&s))
		if err != nil {
			return nil, err
		}
		mine = list
	default:
		return nil, fmt.Errorf("not a snapshot task")
	}
	drop := map[string]bool{}
	for _, x := range Expire(mine, func(x Snap) time.Time { return x.Time }, RetentionOf(&s), time.Now()) {
		drop[x.Name] = true
	}
	for i := range mine {
		mine[i].Keep = !drop[mine[i].Name] || mine[i].Locked
	}
	if mine == nil {
		mine = []Snap{}
	}
	return mine, nil
}

// alreadyRunning recognises "scrub in progress" errors, which are not failures.
func alreadyRunning(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "currently scrubbing") || strings.Contains(m, "already running") ||
		strings.Contains(m, "already in progress") || strings.Contains(m, "device or resource busy")
}

func scrub(s *store.Schedule, resolve Resolver, logf func(string, ...any)) (string, error) {
	var err error
	what := s.Target
	switch s.TargetKind {
	case "mdadm":
		logf("Starting consistency check of RAID %s", s.Target)
		err = storage.RaidAction(s.Target, "check", "", 0)
	case "zfs":
		logf("Starting scrub of pool %s", s.Target)
		err = storage.ZPoolAction(s.Target, "scrub", "", "")
	case "btrfs":
		mp, ok := resolve(s.Target)
		if !ok {
			return "", fmt.Errorf("volume %s no longer exists", s.Target)
		}
		what = mp
		logf("Starting scrub of %s", mp)
		err = storage.BtrfsScrub(mp)
	}
	if err != nil {
		if alreadyRunning(err) {
			return fmt.Sprintf("A check is already running on %s", what), nil
		}
		return "", err
	}
	return fmt.Sprintf("Check started on %s (it continues in the background)", what), nil
}

func smart(s *store.Schedule, logf func(string, ...any)) (string, error) {
	var targets []string
	if s.Target == "all" {
		disks, err := storage.ListDisks()
		if err != nil {
			return "", err
		}
		for _, d := range disks {
			targets = append(targets, d.Path)
		}
	} else {
		targets = strings.Fields(strings.ReplaceAll(s.Target, ",", " "))
	}
	ok, failed := 0, []string{}
	for _, d := range targets {
		logf("SMART %s test on %s", s.SmartTest, d)
		if err := storage.SmartTest(d, s.SmartTest); err != nil {
			logf("  %s: %v", d, err)
			failed = append(failed, d)
			continue
		}
		ok++
	}
	if ok == 0 && len(failed) > 0 {
		return "", fmt.Errorf("no disk accepted the SMART test (%s)", strings.Join(failed, ", "))
	}
	msg := fmt.Sprintf("SMART %s test started on %d disk(s)", s.SmartTest, ok)
	if len(failed) > 0 {
		msg += fmt.Sprintf("; not supported: %s", strings.Join(failed, ", "))
	}
	return msg, nil
}

// appUpdate pulls newer images for the running stacks (or one stack) and
// recreates only the containers whose image changed, then removes the old images.
func appUpdate(ctx context.Context, s *store.Schedule, logf func(string, ...any)) (string, error) {
	names := []string{s.Target}
	if s.Target == "all" {
		names = docker.StackNames()
	}
	var updated, failed []string
	checked := 0
	for _, n := range names {
		if !docker.StackRunning(ctx, n) {
			continue
		}
		checked++
		before := docker.StackImages(ctx, n)
		logf("%s: looking for new images…", n)
		if err := docker.StackAction(ctx, n, "pull", nil); err != nil {
			logf("%s : %v", n, err)
			failed = append(failed, n)
			continue
		}
		if err := docker.StackAction(ctx, n, "up", nil); err != nil {
			logf("%s : %v", n, err)
			failed = append(failed, n)
			continue
		}
		if after := docker.StackImages(ctx, n); after != before {
			updated = append(updated, n)
			logf("%s: update applied", n)
		}
	}
	if len(updated) > 0 {
		docker.PruneDanglingImages(ctx)
	}
	msg := fmt.Sprintf("%d app(s) checked, %d updated", checked, len(updated))
	if len(updated) > 0 {
		msg += " : " + strings.Join(updated, ", ")
	}
	if len(failed) > 0 {
		return msg, fmt.Errorf("update failed for %s", strings.Join(failed, ", "))
	}
	return msg, nil
}
