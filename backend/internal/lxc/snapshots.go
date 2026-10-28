package lxc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"strataos/internal/sys"
)

// Snapshots of the root file system, taken by the storage backend (Btrfs and
// ZFS snapshots, a copy of the image or of the folder otherwise). Their
// metadata is kept in <base>/snapshots/<name>.json, so it moves with the
// container. Restoring keeps the current configuration (limits, network…).

// Snapshot describes a snapshot.
type Snapshot struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Created     time.Time `json:"created"`
	Running     bool      `json:"running"` // taken while the container ran
	Backend     string    `json:"backend"`
}

func validSnap(s string) error {
	if !snapRe.MatchString(s) || strings.HasSuffix(s, ".json") || strings.HasSuffix(s, ".img") || strings.HasPrefix(s, "strata-") {
		return fmt.Errorf("invalid snapshot name (letters, digits, . - _)")
	}
	return nil
}

func snapshotMetas(s *Spec) []Snapshot {
	entries, _ := os.ReadDir(s.Storage.snapDir())
	out := []Snapshot{}
	for _, e := range entries {
		n, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || validSnap(n) != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.Storage.snapDir(), e.Name()))
		if err != nil {
			continue
		}
		var sn Snapshot
		if json.Unmarshal(b, &sn) == nil && sn.Name == n {
			out = append(out, sn)
		}
	}
	slices.SortFunc(out, func(a, b Snapshot) int { return a.Created.Compare(b.Created) })
	return out
}

// Snapshots lists the snapshots of a container, oldest first.
func Snapshots(name string) ([]Snapshot, error) {
	s, err := Load(name)
	if err != nil {
		return nil, err
	}
	return snapshotMetas(s), nil
}

// CreateSnapshot takes a snapshot (the container may run).
func CreateSnapshot(ctx context.Context, name, snap, desc string) error {
	if err := validSnap(snap); err != nil {
		return err
	}
	unlock := lock(name)
	if unlock == nil {
		return busy(name)
	}
	defer unlock()
	s, err := Load(name)
	if err != nil {
		return err
	}
	meta := filepath.Join(s.Storage.snapDir(), snap+".json")
	if _, err := os.Stat(meta); err == nil {
		return fmt.Errorf("snapshot %s already exists", snap)
	}
	be, err := BackendFor(s.Storage.Backend)
	if err != nil {
		return err
	}
	if err := be.Prepare(ctx, &s.Storage); err != nil {
		return err
	}
	sn := Snapshot{Name: snap, Description: cleanText(desc, 300), Created: time.Now().UTC().Truncate(time.Second), Running: State(name) == "running", Backend: s.Storage.Backend}
	if err := os.MkdirAll(s.Storage.snapDir(), 0o700); err != nil {
		return err
	}
	if err := be.Snapshot(ctx, &s.Storage, snap); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(sn, "", "  ")
	return sys.WriteFileAtomic(meta, b, 0o600)
}

// RestoreSnapshot brings a stopped container back to a snapshot. On ZFS the
// snapshots taken after it are removed (ZFS can only roll back to the latest).
func RestoreSnapshot(ctx context.Context, name, snap string) error {
	if err := validSnap(snap); err != nil {
		return err
	}
	unlock := lock(name)
	if unlock == nil {
		return busy(name)
	}
	defer unlock()
	s, err := Load(name)
	if err != nil {
		return err
	}
	if State(name) != "stopped" {
		return fmt.Errorf("stop %s first", name)
	}
	metas := snapshotMetas(s)
	i := slices.IndexFunc(metas, func(m Snapshot) bool { return m.Name == snap })
	if i < 0 {
		return fmt.Errorf("snapshot %s not found", snap)
	}
	be, err := BackendFor(s.Storage.Backend)
	if err != nil {
		return err
	}
	if err := be.Prepare(ctx, &s.Storage); err != nil {
		return err
	}
	if err := be.Restore(ctx, &s.Storage, snap); err != nil {
		return err
	}
	if s.Storage.Backend == "zfs" {
		for _, m := range metas[i+1:] {
			_ = os.Remove(filepath.Join(s.Storage.snapDir(), m.Name+".json"))
		}
	}
	// The configuration is the current one: keep the container's own network files in line.
	_ = applyGuestNetwork(s)
	return nil
}

// DeleteSnapshot removes a snapshot.
func DeleteSnapshot(ctx context.Context, name, snap string) error {
	if err := validSnap(snap); err != nil {
		return err
	}
	unlock := lock(name)
	if unlock == nil {
		return busy(name)
	}
	defer unlock()
	s, err := Load(name)
	if err != nil {
		return err
	}
	meta := filepath.Join(s.Storage.snapDir(), snap+".json")
	if _, err := os.Stat(meta); err != nil {
		return fmt.Errorf("snapshot %s not found", snap)
	}
	be, err := BackendFor(s.Storage.Backend)
	if err != nil {
		return err
	}
	if err := be.DeleteSnapshot(ctx, &s.Storage, snap); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Remove(meta)
}
