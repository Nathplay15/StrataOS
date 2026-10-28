package lxc

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Containers marked "start with the NAS" are started by StrataOS once per
// boot, by order, waiting each one's delay before the next (their settings
// live in container.json and move with them). systemd stops them when the
// NAS shuts down, before their volumes are unmounted.

// BootMarker records that the containers were started during this boot, so
// that a restart of StrataOS does not start again those stopped by hand.
var BootMarker = "/run/strata/lxc-autostart.done"

func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

// StartedThisBoot reports whether StartAll already ran since the NAS booted.
func StartedThisBoot() bool {
	b, err := os.ReadFile(BootMarker)
	return err == nil && strings.TrimSpace(string(b)) == bootID()
}

// MarkStarted records the boot.
func MarkStarted() {
	_ = os.MkdirAll(filepath.Dir(BootMarker), 0o755)
	_ = os.WriteFile(BootMarker, []byte(bootID()+"\n"), 0o644)
}

// startOrder returns the autostart containers by order, then name.
func startOrder(list []Spec) []Spec {
	var out []Spec
	for _, s := range list {
		if s.Autostart {
			out = append(out, s)
		}
	}
	slices.SortStableFunc(out, func(a, b Spec) int {
		if a.AutostartOrder != b.AutostartOrder {
			return a.AutostartOrder - b.AutostartOrder
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// StartAll starts the containers marked to start with the NAS. Volumes may be
// mounted a little after StrataOS starts (encrypted ones are unlocked later):
// a container whose storage is missing is retried for a few minutes.
func StartAll(ctx context.Context, report func(name string, err error)) {
	var specs []Spec
	for _, n := range Names() {
		if s, err := Load(n); err == nil {
			specs = append(specs, *s)
		}
	}
	for _, s := range startOrder(specs) {
		if ctx.Err() != nil {
			return
		}
		if State(s.Name) == "running" {
			continue
		}
		var err error
		for try := 0; try < 10; try++ {
			if err = Start(ctx, s.Name); err == nil {
				break
			}
			if _, serr := os.Stat(s.Storage.Base); serr == nil {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
		report(s.Name, err)
		if err == nil && s.AutostartDelay > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(min(s.AutostartDelay, 3600)) * time.Second):
			}
		}
	}
}
