package vms

// Live image backups through libvirt's backup API (pull mode): libvirt
// makes a point-in-time view of the disks of a running VM (copy before
// write into a scratch file) and serves it over NBD, along with the blocks
// changed since a checkpoint (a persistent QEMU dirty bitmap in each qcow2
// file). Each backup task keeps its own checkpoints, named
// strata-<task>-<unix time>; libvirt keeps the bitmaps right across
// external snapshots, commits and restarts, and refuses a checkpoint whose
// bitmap is missing or inconsistent (QEMU killed, image changed elsewhere):
// the backup then reads the disks in full.

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// CheckpointRe matches the names of the checkpoints StrataOS makes.
var CheckpointRe = regexp.MustCompile(`^strata-[A-Za-z0-9]{1,40}-[0-9]{1,20}$`)

// CheckpointPrefix is the start of the checkpoint names of a backup task.
func CheckpointPrefix(task string) string { return "strata-" + task + "-" }

// LiveBackupDisk is a disk that can be read by a live backup.
func LiveBackupDisk(d Disk) bool {
	return d.Device == "disk" && d.Format == "qcow2" && filepath.IsAbs(d.Source) && !strings.ContainsAny(d.Source, ",\n\r'\"<>&")
}

// LiveBackupSupported tells whether libvirt can run pull-mode backups.
func LiveBackupSupported() bool {
	out, err := virsh("domcapabilities")
	return err == nil && strings.Contains(out, "<backup supported='yes'/>")
}

// Checkpoints lists the checkpoints of a VM.
func Checkpoints(name string) ([]string, error) {
	out, err := virsh("checkpoint-list", name, "--name")
	if err != nil {
		return nil, err
	}
	var cps []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			cps = append(cps, l)
		}
	}
	return cps, nil
}

// HasCheckpoint tells whether a VM still has a checkpoint.
func HasCheckpoint(name, cp string) bool {
	cps, err := Checkpoints(name)
	if err != nil {
		return false
	}
	for _, c := range cps {
		if c == cp {
			return true
		}
	}
	return false
}

// DeleteCheckpoint removes a checkpoint (its bitmap is merged into the
// previous one by libvirt); when the bitmap is already gone, only the
// metadata.
func DeleteCheckpoint(name, cp string) error {
	if !CheckpointRe.MatchString(cp) {
		return fmt.Errorf("invalid checkpoint %q", cp)
	}
	if _, err := virsh("checkpoint-delete", name, cp); err != nil {
		if _, err2 := virsh("checkpoint-delete", name, cp, "--metadata"); err2 != nil {
			return err
		}
	}
	return nil
}

// DropCheckpoints deletes the checkpoints whose names start with prefix,
// except keep.
func DropCheckpoints(name, prefix, keep string) {
	cps, _ := Checkpoints(name)
	// Newest first: deleting the leaf of the chain is the cheapest.
	for i := len(cps) - 1; i >= 0; i-- {
		if c := cps[i]; strings.HasPrefix(c, prefix) && c != keep && CheckpointRe.MatchString(c) {
			_ = DeleteCheckpoint(name, c)
		}
	}
}

// Quiesce freezes the guest file systems through the guest agent; thaw
// must be called. Without an agent the copy is crash-consistent.
func Quiesce(name string, p Progress) (thaw func(), consistency string) {
	if _, err := virsh("domfsfreeze", name); err != nil {
		p.log("Guest agent unavailable (%v): crash-consistent copy", firstLine(err.Error()))
		return func() {}, "crash"
	}
	return func() { _, _ = virsh("domfsthaw", name) }, "frozen"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// LiveBackup is a running pull-mode backup job.
type LiveBackup struct {
	VM          string
	Socket      string // NBD server: export = disk target
	Checkpoint  string // created with the job
	Since       string // incremental from this checkpoint ("" = full)
	Consistency string
	scratch     []string
}

// BitmapContext is the NBD meta context of the changed blocks of a disk.
func (b *LiveBackup) BitmapContext(target string) string {
	return "qemu:dirty-bitmap:backup-" + target
}

// domainDir is the private folder libvirt keeps for a running VM; its
// AppArmor profile lets QEMU create the NBD socket there.
func domainDir(name string) (string, error) {
	id, err := virsh("domid", name)
	if err != nil {
		return "", err
	}
	id = strings.TrimSpace(id)
	if id == "" || id == "-" || strings.ContainsAny(id, "/.*") {
		return "", fmt.Errorf("%s is not running", name)
	}
	m, _ := filepath.Glob("/var/lib/libvirt/qemu/domain-" + id + "-*")
	if len(m) != 1 {
		return "", fmt.Errorf("the runtime folder of %s was not found", name)
	}
	return m[0], nil
}

var diskTargetRe = regexp.MustCompile(`^[a-z]{2,4}[a-z0-9]{1,4}$`)

// BeginLiveBackup starts a pull-mode backup of disks (targets with their
// image path) creating the checkpoint cp, incremental from since when set.
// The guest file systems are frozen while the job starts.
func BeginLiveBackup(name string, disks map[string]string, cp, since string, p Progress) (*LiveBackup, error) {
	if !CheckpointRe.MatchString(cp) || (since != "" && !CheckpointRe.MatchString(since)) {
		return nil, fmt.Errorf("invalid checkpoint name")
	}
	dir, err := domainDir(name)
	if err != nil {
		return nil, err
	}
	b := &LiveBackup{VM: name, Socket: filepath.Join(dir, "strata-backup.sock"), Checkpoint: cp, Since: since}
	var bx, cx strings.Builder
	bx.WriteString("<domainbackup mode='pull'>")
	if since != "" {
		bx.WriteString("<incremental>" + since + "</incremental>")
	}
	fmt.Fprintf(&bx, "<server transport='unix' socket='%s'/><disks>", html.EscapeString(b.Socket))
	fmt.Fprintf(&cx, "<domaincheckpoint><name>%s</name><description>StrataOS image backup</description><disks>", cp)
	for t, src := range disks {
		if !diskTargetRe.MatchString(t) || !LiveBackupDisk(Disk{Device: "disk", Format: "qcow2", Source: src}) {
			return nil, fmt.Errorf("disk %s cannot be backed up live", t)
		}
		scratch := filepath.Join(filepath.Dir(src), fmt.Sprintf(".%s-%s.strata-scratch.qcow2", name, t))
		_ = os.Remove(scratch) // left by a crash
		b.scratch = append(b.scratch, scratch)
		bitmap := ""
		if since != "" {
			bitmap = " exportbitmap='backup-" + t + "'"
		}
		fmt.Fprintf(&bx, "<disk name='%s' backup='yes' type='file' exportname='%s'%s><scratch file='%s'/></disk>", t, t, bitmap, html.EscapeString(scratch))
		fmt.Fprintf(&cx, "<disk name='%s' checkpoint='bitmap'/>", t)
	}
	bx.WriteString("</disks></domainbackup>")
	cx.WriteString("</disks></domaincheckpoint>")
	if err := xml.Unmarshal([]byte(bx.String()), new(struct{})); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "strata-backup-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	bf, cf := filepath.Join(tmp, "backup.xml"), filepath.Join(tmp, "checkpoint.xml")
	if err := os.WriteFile(bf, []byte(bx.String()), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(cf, []byte(cx.String()), 0o600); err != nil {
		return nil, err
	}
	_ = os.Remove(b.Socket)
	thaw, consistency := Quiesce(name, p)
	_, err = virsh("backup-begin", name, bf, cf)
	thaw()
	if err != nil {
		b.cleanScratch()
		return nil, fmt.Errorf("live backup: %s", firstLine(err.Error()))
	}
	b.Consistency = consistency
	for range 100 { // the socket appears with the job
		if _, err := os.Stat(b.Socket); err == nil {
			return b, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	b.End()
	return nil, fmt.Errorf("live backup: the NBD export did not appear")
}

func (b *LiveBackup) cleanScratch() {
	for _, s := range b.scratch {
		_ = os.Remove(s)
	}
}

// End stops the job (the point-in-time view and its scratch files go away).
func (b *LiveBackup) End() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for ctx.Err() == nil {
		if _, err := virsh("domjobabort", b.VM); err != nil {
			break // no job left
		}
		time.Sleep(200 * time.Millisecond)
	}
	b.cleanScratch()
	_ = os.Remove(b.Socket)
}
