package scheduler

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"strataos/internal/antivirus"
	"strataos/internal/docker"
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
		if s.KeepCount < 0 || s.KeepDays < 0 {
			return fmt.Errorf("retention values must be positive")
		}
		if s.KeepCount == 0 && s.KeepDays == 0 {
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
		default:
			return fmt.Errorf("snapshots are supported on ZFS datasets and Btrfs volumes")
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

func zfsSnapshot(s *store.Schedule, logf func(string, ...any)) (string, error) {
	name := autoPrefix(s) + time.Now().Format(stampLayout)
	logf("Creating %s@%s", s.Target, name)
	if err := storage.Snapshot(s.Target, name, s.Recursive); err != nil {
		return "", err
	}
	if s.LockDays > 0 {
		if err := storage.LockZFSSnapshot(s.Target+"@"+name, time.Now().AddDate(0, 0, s.LockDays), s.Recursive); err != nil {
			return "", fmt.Errorf("snapshot created but not locked: %w", err)
		}
	}
	all, err := storage.SnapshotsOf(s.Target)
	if err != nil {
		return "", err
	}
	var mine []storage.ZSnapshot
	for _, sn := range all {
		if strings.HasPrefix(sn.Name, s.Target+"@"+autoPrefix(s)) {
			mine = append(mine, sn)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Creation.After(mine[j].Creation) })
	removed := 0
	for _, sn := range expired(mine, func(z storage.ZSnapshot) time.Time { return z.Creation }, s.KeepCount, s.KeepDays, time.Now()) {
		if time.Now().Before(storage.ZFSLockUntil(sn.Name)) {
			continue // still locked: kept until its date
		}
		logf("Retention: destroying %s", sn.Name)
		if err := storage.DestroyDataset(sn.Name, s.Recursive); err != nil {
			logf("warning: %v", err)
			continue
		}
		removed++
	}
	return fmt.Sprintf("Snapshot %s@%s créé, %d ancien(s) supprimé(s), %d conservé(s)", s.Target, name, removed, len(mine)-removed), nil
}

func btrfsSnapshot(s *store.Schedule, mp string, logf func(string, ...any)) (string, error) {
	base := "root"
	if s.Subvolume != "" {
		base = strings.ReplaceAll(strings.Trim(s.Subvolume, "/"), "/", "_")
	}
	prefix := base + "@" + autoPrefix(s)
	name := prefix + time.Now().Format(stampLayout)
	logf("Creating %s/.snapshots/%s", mp, name)
	if _, err := storage.BtrfsSnapshot(mp, s.Subvolume, name); err != nil {
		return "", err
	}
	if s.LockDays > 0 {
		if err := storage.LockBtrfsSnapshot(mp, name, time.Now().AddDate(0, 0, s.LockDays)); err != nil {
			return "", fmt.Errorf("snapshot created but not locked: %w", err)
		}
	}
	names, err := storage.BtrfsSnapshotNames(mp)
	if err != nil {
		return "", err
	}
	type snap struct {
		name string
		at   time.Time
	}
	var mine []snap
	for _, n := range names {
		if !strings.HasPrefix(n, prefix) {
			continue
		}
		at, err := time.ParseInLocation(stampLayout, strings.TrimPrefix(n, prefix), time.Local)
		if err != nil {
			continue
		}
		mine = append(mine, snap{n, at})
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].at.After(mine[j].at) })
	removed := 0
	for _, sn := range expired(mine, func(x snap) time.Time { return x.at }, s.KeepCount, s.KeepDays, time.Now()) {
		if time.Now().Before(storage.BtrfsLockUntil(mp, sn.name)) {
			continue // still locked: kept until its date
		}
		logf("Retention: deleting .snapshots/%s", sn.name)
		if err := storage.BtrfsDeleteSubvol(mp, ".snapshots/"+sn.name); err != nil {
			logf("warning: %v", err)
			continue
		}
		removed++
	}
	return fmt.Sprintf("Snapshot .snapshots/%s créé, %d ancien(s) supprimé(s), %d conservé(s)", name, removed, len(mine)-removed), nil
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
			return fmt.Sprintf("Vérification déjà en cours sur %s", what), nil
		}
		return "", err
	}
	return fmt.Sprintf("Vérification lancée sur %s (elle se poursuit en arrière-plan)", what), nil
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
	msg := fmt.Sprintf("Test SMART %s lancé sur %d disque(s)", s.SmartTest, ok)
	if len(failed) > 0 {
		msg += fmt.Sprintf(" ; non pris en charge : %s", strings.Join(failed, ", "))
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
		logf("%s : recherche de nouvelles images…", n)
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
			logf("%s : mise à jour appliquée", n)
		}
	}
	if len(updated) > 0 {
		docker.PruneDanglingImages(ctx)
	}
	msg := fmt.Sprintf("%d application(s) vérifiée(s), %d mise(s) à jour", checked, len(updated))
	if len(updated) > 0 {
		msg += " : " + strings.Join(updated, ", ")
	}
	if len(failed) > 0 {
		return msg, fmt.Errorf("update failed for %s", strings.Join(failed, ", "))
	}
	return msg, nil
}
