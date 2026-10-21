package vms

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"strataos/internal/sys"
)

// Autostart: StrataOS starts the VMs marked "start with the NAS" itself, in
// order and with a delay after each one, and shuts them down in reverse order
// when the NAS stops (strata-vms.service). libvirt's own autostart is turned
// off for them so that a single owner decides.

// AutostartEntry is one VM to start with the NAS.
type AutostartEntry struct {
	Name  string `json:"name"`
	Order int    `json:"order"`
	Delay int    `json:"delay"` // seconds to wait after starting it, before the next one
}

// StartOrder sorts entries by order, then name.
func StartOrder(list []AutostartEntry) []AutostartEntry {
	out := append([]AutostartEntry(nil), list...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ShutdownOrder is the reverse of StartOrder.
func ShutdownOrder(list []AutostartEntry) []AutostartEntry {
	out := StartOrder(list)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// DisableLibvirtAutostart makes StrataOS the only owner of a VM's autostart.
func DisableLibvirtAutostart(name string) {
	_, _ = virsh("autostart", name, "--disable")
}

// LibvirtReady waits until libvirt answers (at boot it may start after StrataOS).
func LibvirtReady(ctx context.Context, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if _, err := virsh("list", "--name"); err == nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(3 * time.Second):
		}
	}
}

// StartAll starts the entries in order. report is called for each VM.
func StartAll(ctx context.Context, list []AutostartEntry, report func(name string, err error)) {
	for _, e := range StartOrder(list) {
		if ctx.Err() != nil {
			return
		}
		state, err := virsh("domstate", e.Name)
		if err != nil {
			report(e.Name, err)
			continue
		}
		if strings.TrimSpace(state) == "running" {
			continue
		}
		err = Action(e.Name, "start")
		report(e.Name, err)
		if err == nil && e.Delay > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(min(e.Delay, 3600)) * time.Second):
			}
		}
	}
}

// ShutdownAll stops the entries in reverse order, each with an ACPI shutdown
// and up to timeout to stop; a VM that does not stop is left to libvirt-guests.
func ShutdownAll(list []AutostartEntry, timeout time.Duration, logf func(string, ...any)) {
	for _, e := range ShutdownOrder(list) {
		state, err := virsh("domstate", e.Name)
		if err != nil || strings.TrimSpace(state) != "running" {
			continue
		}
		logf("shutting down %s", e.Name)
		if _, err := virsh("shutdown", e.Name); err != nil {
			logf("%s: %v", e.Name, err)
			continue
		}
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if s, err := virsh("domstate", e.Name); err != nil || strings.TrimSpace(s) == "shut off" {
				break
			}
			time.Sleep(2 * time.Second)
		}
	}
}

// BootMarker records that the VMs were started during this boot, so that a
// restart of the daemon does not start again VMs stopped by hand since.
var BootMarker = "/run/strata/vm-autostart.done"

func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

// StartedThisBoot reports whether StartAll already ran since the NAS booted.
func StartedThisBoot() bool {
	b, err := os.ReadFile(BootMarker)
	return err == nil && strings.TrimSpace(string(b)) == bootID()
}

func MarkStarted() {
	_ = os.MkdirAll(filepath.Dir(BootMarker), 0o755)
	_ = os.WriteFile(BootMarker, []byte(bootID()+"\n"), 0o644)
}

const shutdownUnit = "/etc/systemd/system/strata-vms.service"

// shutdownUnitText runs `strata -vm-shutdown` when the NAS stops. Ordered after
// libvirtd and libvirt-guests, it is stopped before them: the ordered VMs are
// shut down first, libvirt-guests then handles any other VM as configured.
const shutdownUnitText = `[Unit]
Description=StrataOS virtual machines (ordered shutdown)
After=libvirtd.service virtqemud.service libvirt-guests.service
Wants=libvirtd.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/true
ExecStop=/usr/bin/strata -vm-shutdown
TimeoutStopSec=900

[Install]
WantedBy=multi-user.target
`

// EnsureShutdownUnit installs and starts the ordered-shutdown unit.
func EnsureShutdownUnit() error {
	if b, err := os.ReadFile(shutdownUnit); err == nil && string(b) == shutdownUnitText {
		if out, _ := sys.Run("systemctl", "is-active", "strata-vms.service"); strings.TrimSpace(out) == "active" {
			return nil
		}
	} else if err := sys.WriteFileAtomic(shutdownUnit, []byte(shutdownUnitText), 0o644); err != nil {
		return err
	}
	_, _ = sys.Run("systemctl", "daemon-reload")
	_, err := sys.Run("systemctl", "enable", "--now", "strata-vms.service")
	return err
}
