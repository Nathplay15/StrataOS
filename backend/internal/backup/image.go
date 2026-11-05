package backup

// Image backups to a datastore: each disk of a VM is read as a raw stream
// (qemu-nbd, see nbd.go) and each container as an uncompressed tar, and fed
// to "restic backup --stdin". restic cuts the stream into content-defined
// chunks, so blocks shared by several VMs, containers or runs are stored
// once. One backup of a guest is several snapshots taken with the same
// time and tags: sg-<vm|ct>-<name> names the guest, sp-<part> the part
// (disk-<target>, then config for a VM: the definition, written last so
// that a complete backup is one with its config; rootfs for a container).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"strataos/internal/lxc"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/vms"
)

// countingReader reports the progress of a stream of known size.
type countingReader struct {
	r    io.Reader
	n    int64
	size int64
	set  func(float64)
	last time.Time
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.size > 0 && c.set != nil && time.Since(c.last) > time.Second {
		c.last = time.Now()
		c.set(min(100, float64(c.n)*100/float64(c.size)))
	}
	return n, err
}

type stdinSummary struct {
	SnapshotID      string `json:"snapshot_id"`
	DataAddedPacked int64  `json:"data_added_packed"`
	TotalBytes      int64  `json:"total_bytes_processed"`
}

// backupStream stores r as one file of a new snapshot.
func backupStream(ctx context.Context, t *store.BackupTask, r io.Reader, file string, at time.Time, tags []string, p Progress) (stdinSummary, error) {
	var sum stdinSummary
	args := []string{"backup", "--json", "--retry-lock", "30m", "--stdin", "--stdin-filename", file, "--host", hostname(),
		"--time", at.Local().Format("2006-01-02 15:04:05"), "--tag", "strata-" + t.ID}
	for _, tg := range tags {
		args = append(args, "--tag", tg)
	}
	lw := &lineWriter{fn: func(l string) {
		var m struct {
			Type    string `json:"message_type"`
			Message string `json:"message"`
			Error   any    `json:"error"`
		}
		if json.Unmarshal([]byte(l), &m) != nil {
			if strings.TrimSpace(l) != "" {
				p.Log("%s", l)
			}
			return
		}
		switch m.Type {
		case "summary":
			_ = json.Unmarshal([]byte(l), &sum)
		case "error":
			p.Log("%v", m.Error)
		case "exit_error":
			p.Log("%s", m.Message)
		}
	}}
	if t.DestType == "strataos" {
		px, err := startVaultProxy(t)
		if err != nil {
			return sum, err
		}
		defer px.close()
		c := *t
		c.DestType, c.DestPath = "rest", px.url
		t = &c
	}
	_, err := sys.RunOpts(ctx, sys.Opts{Env: resticEnv(t), Timeout: 48 * time.Hour, Output: lw, Input: r}, "restic", resticArgs(t, args...)...)
	if err == nil && sum.SnapshotID == "" {
		err = fmt.Errorf("restic gave no snapshot")
	}
	return sum, err
}

// forgetIDs removes the snapshots of an unfinished guest backup.
func forgetIDs(ctx context.Context, t *store.BackupTask, ids []string) {
	if len(ids) == 0 || t.AppendOnly {
		return
	}
	_, _ = restic(context.WithoutCancel(ctx), t, nil, append([]string{"forget", "--retry-lock", "30m"}, ids...)...)
}

// runDatastore runs a task whose repository is a datastore: its VMs and
// containers as images, then its folders as one usual snapshot.
func runDatastore(ctx context.Context, t *store.BackupTask, p Progress) (string, error) {
	if t.DestType == "datastore" {
		return "", fmt.Errorf("the backup repository of this task no longer exists")
	}
	if err := ensureRepo(ctx, t, p); err != nil {
		return "", err
	}
	start := time.Now()
	at := start.Truncate(time.Second)
	steps := len(t.VMs) + len(t.Containers)
	if len(t.Sources) > 0 {
		steps++
	}
	step := 0
	sub := func() Progress {
		from := float64(step) * 95 / float64(steps)
		to := float64(step+1) * 95 / float64(steps)
		return Progress{Log: p.Log, Set: func(v float64) { p.Set(from + (to-from)*v/100) }}
	}
	var failed []string
	var logical, added int64
	for _, n := range t.VMs {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		l, a, err := backupVMImage(ctx, t, n, at, sub())
		step++
		if err != nil {
			p.Log("VM %s: %v", n, err)
			failed = append(failed, fmt.Sprintf("VM %s: %v", n, err))
			continue
		}
		logical, added = logical+l, added+a
	}
	for _, n := range t.Containers {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		l, a, err := backupContainerImage(ctx, t, n, at, sub())
		step++
		if err != nil {
			p.Log("Container %s: %v", n, err)
			failed = append(failed, fmt.Sprintf("container %s: %v", n, err))
			continue
		}
		logical, added = logical+l, added+a
	}
	msg := ""
	if steps > len(t.Sources) && len(failed) < len(t.VMs)+len(t.Containers) {
		msg = fmt.Sprintf("%d VM(s) and container(s) backed up: %s read, %s new data stored", len(t.VMs)+len(t.Containers)-len(failed), humanBytes(logical), humanBytes(added))
	}
	if len(t.Sources) > 0 {
		m, err := resticFolders(ctx, t, sub())
		if err != nil {
			failed = append(failed, err.Error())
		} else if msg == "" {
			msg = m
		} else {
			msg += "; " + m
		}
	}
	p.Set(96)
	applyRetention(ctx, t, p)
	if len(failed) > 0 {
		return "", fmt.Errorf("%s", strings.Join(failed, "; "))
	}
	return fmt.Sprintf("%s (%s)", msg, time.Since(start).Round(time.Second)), nil
}

// backupVMImage stores the disks of a VM, then its definition.
func backupVMImage(ctx context.Context, t *store.BackupTask, name string, at time.Time, p Progress) (logical, added int64, err error) {
	if !sys.Has("qemu-nbd") {
		return 0, 0, fmt.Errorf("qemu-nbd is not installed")
	}
	p.Log("Backing up virtual machine %s", name)
	src, err := vms.OpenForImage(ctx, name, t.VMMode, vms.Progress{Log: p.Log})
	if err != nil {
		return 0, 0, err
	}
	released := false
	release := func() {
		if !released {
			released = true
			src.Release()
		}
	}
	defer release()
	b := &src.Bundle
	guest := guestTag + "vm-" + name
	var ids []string
	defer func() {
		if err != nil {
			forgetIDs(ctx, t, ids)
		}
	}()
	for i, d := range b.Disks {
		conn, closeImg, err := openImage(ctx, d.Source, d.Format, false)
		if err != nil {
			return 0, 0, fmt.Errorf("disk %s: %w", d.Target, err)
		}
		b.Disks[i].Size = conn.size
		from, to := float64(i)*95/float64(len(b.Disks)), float64(i+1)*95/float64(len(b.Disks))
		cr := &countingReader{r: &nbdReader{n: conn}, size: conn.size, set: func(v float64) { p.Set(from + (to-from)*v/100) }}
		p.Log("Disk %s: %s (%s)", d.Target, humanBytes(conn.size), b.Manifest.Consistency)
		sum, err := backupStream(ctx, t, cr, d.Target+".raw", at, []string{guest, partTag + "disk-" + d.Target}, p)
		closeImg()
		if err == nil && cr.n != conn.size {
			err = fmt.Errorf("read %d of %d bytes", cr.n, conn.size)
		}
		if sum.SnapshotID != "" {
			ids = append(ids, sum.SnapshotID)
		}
		if err != nil {
			return 0, 0, fmt.Errorf("disk %s: %w", d.Target, err)
		}
		logical += sum.TotalBytes
		added += sum.DataAddedPacked
		p.Log("Disk %s: %s new data stored", d.Target, humanBytes(sum.DataAddedPacked))
	}
	release()
	b.Manifest.Size = logical
	data, err := json.Marshal(b)
	if err != nil {
		return 0, 0, err
	}
	sum, err := backupStream(ctx, t, bytes.NewReader(data), "guest.json", at, []string{guest, partTag + "config"}, p)
	if sum.SnapshotID != "" {
		ids = append(ids, sum.SnapshotID)
	}
	if err != nil {
		return 0, 0, err
	}
	p.Set(100)
	return logical, added + sum.DataAddedPacked, nil
}

// backupContainerImage stores a container as an uncompressed archive.
func backupContainerImage(ctx context.Context, t *store.BackupTask, name string, at time.Time, p Progress) (int64, int64, error) {
	p.Log("Backing up container %s", name)
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := lxc.ExportStream(ctx, name, "", pw, false, p.Log)
		pw.CloseWithError(err)
		done <- err
	}()
	sum, err := backupStream(ctx, t, pr, "rootfs.tar", at, []string{guestTag + "ct-" + name, partTag + "rootfs"}, p)
	pr.CloseWithError(io.ErrClosedPipe) // unblocks tar when restic stopped early
	if xerr := <-done; err == nil && xerr != nil {
		err = xerr
	}
	if err != nil {
		if sum.SnapshotID != "" {
			forgetIDs(ctx, t, []string{sum.SnapshotID})
		}
		return 0, 0, err
	}
	p.Log("Container %s: %s, %s new data stored", name, humanBytes(sum.TotalBytes), humanBytes(sum.DataAddedPacked))
	p.Set(100)
	return sum.TotalBytes, sum.DataAddedPacked, nil
}

// ---- Listing and restoring guests ----

// GuestBackups lists the VM and container backups of a task on this NAS.
func GuestBackups(ctx context.Context, t store.BackupTask) ([]Item, error) {
	snaps, err := taskSnapshots(ctx, &t)
	if err != nil {
		return nil, err
	}
	out := []Item{}
	for _, it := range GroupSnapshots(snaps) {
		if it.Kind != "folders" {
			out = append(out, it)
		}
	}
	return out, nil
}

// findGuest finds the backup a snapshot (any part) belongs to, in any
// task or NAS of the repository.
func findGuest(ctx context.Context, t *store.BackupTask, id string) (*Item, error) {
	if !snapIDRe.MatchString(id) {
		return nil, fmt.Errorf("invalid snapshot id")
	}
	one, err := allSnapshots(ctx, t, id)
	if err != nil {
		return nil, err
	}
	if len(one) != 1 || tagValue(one[0].Tags, guestTag) == "" {
		return nil, fmt.Errorf("this snapshot holds no VM or container")
	}
	s := one[0]
	all, err := allSnapshots(ctx, t, "--host", s.Hostname, "--tag", guestTag+tagValue(s.Tags, guestTag))
	if err != nil {
		return nil, err
	}
	for _, it := range GroupSnapshots(all) {
		for _, pt := range it.Parts {
			if pt.ID == s.ID {
				if !it.Complete {
					return nil, fmt.Errorf("this backup is incomplete")
				}
				return &it, nil
			}
		}
	}
	return nil, fmt.Errorf("backup not found")
}

// GuestRestore says how to bring a guest back.
type GuestRestore struct {
	Name string `json:"name"`
	// Storage: folder of the VM (its volume, e.g. /srv/strata/main/vms), or
	// the location of a container ("" = where it was).
	Storage string `json:"storage"`
	Backend string `json:"backend,omitempty"` // container storage
	// Replace: a guest of that name (stopped) is deleted once the backup
	// has been read back, then recreated from it.
	Replace bool `json:"replace"`
	Start   bool `json:"start"`
}

var guestNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// RestoreGuest recreates a VM or container from an image backup (id: any
// snapshot of it).
func RestoreGuest(ctx context.Context, t store.BackupTask, id string, r GuestRestore, p Progress) (string, error) {
	it, err := findGuest(ctx, &t, id)
	if err != nil {
		return "", err
	}
	if r.Name == "" {
		r.Name = it.Name
	}
	if !guestNameRe.MatchString(r.Name) {
		return "", fmt.Errorf("invalid name")
	}
	if it.Kind == "vm" {
		return restoreVMImage(ctx, &t, it, r, p)
	}
	return restoreContainerImage(ctx, &t, it, r, p)
}

// stagingVolume is the volume a restore is staged on: the one of pref, or
// the mounted volume with the most free space.
func stagingVolume(pref string) (string, error) {
	if pref != "" {
		v := vms.VolumeRoot(pref)
		if storage.IsMountpoint(v) {
			return v, nil
		}
	}
	entries, _ := os.ReadDir(storage.MountRoot)
	best, free := "", uint64(0)
	for _, e := range entries {
		p := filepath.Join(storage.MountRoot, e.Name())
		if !e.IsDir() || !storage.IsMountpoint(p) {
			continue
		}
		var st syscall.Statfs_t
		if syscall.Statfs(p, &st) == nil && st.Bavail*uint64(st.Bsize) > free {
			best, free = p, st.Bavail*uint64(st.Bsize)
		}
	}
	if best == "" {
		return "", fmt.Errorf("no volume to restore to")
	}
	return best, nil
}

func freeBytes(p string) int64 {
	var st syscall.Statfs_t
	if syscall.Statfs(p, &st) != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// dumpTo writes one file of a snapshot to w.
func dumpTo(ctx context.Context, t *store.BackupTask, snap, file string, w io.Writer) error {
	return withRepo(*t, func(t *store.BackupTask) error {
		return sys.Stream(ctx, sys.Opts{Env: resticEnv(t), Timeout: 48 * time.Hour}, w, "restic", resticArgs(t, "--no-lock", "dump", snap, file)...)
	})
}

// readBundle reads the definition of a VM backup.
func readGuestBundle(ctx context.Context, t *store.BackupTask, snap string) (*vms.Bundle, error) {
	var buf bytes.Buffer
	if err := dumpTo(ctx, t, snap, "/guest.json", &limitWriter{w: &buf, n: 64 << 20}); err != nil {
		return nil, err
	}
	var b vms.Bundle
	if err := json.Unmarshal(buf.Bytes(), &b); err != nil {
		return nil, fmt.Errorf("invalid VM backup: %v", err)
	}
	return &b, nil
}

type limitWriter struct {
	w io.Writer
	n int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.n {
		return 0, fmt.Errorf("too large")
	}
	l.n -= int64(len(p))
	return l.w.Write(p)
}

var diskTargetRe = regexp.MustCompile(`^[a-z]{2,4}[a-z0-9]{1,4}$`)

// writeDisk writes a disk image of a snapshot into a new qcow2 file.
func writeDisk(ctx context.Context, t *store.BackupTask, snap, target, dst string, size int64, p Progress) error {
	if _, err := sys.RunCtx(ctx, "qemu-img", "create", "-q", "-f", "qcow2", "--", dst, strconv64(size)); err != nil {
		return err
	}
	conn, closeImg, err := openImage(ctx, dst, "qcow2", true)
	if err != nil {
		return err
	}
	defer closeImg()
	zw := newZeroWriter(conn)
	vol := filepath.Dir(dst)
	zw.check = func(n int64) error {
		p.Set(min(100, float64(n)*100/float64(max(size, 1))))
		if f := freeBytes(vol); f >= 0 && f < 1<<30 {
			return fmt.Errorf("less than 1 GB left on %s", vol)
		}
		return nil
	}
	if err := dumpTo(ctx, t, snap, "/"+target+".raw", zw); err != nil {
		return err
	}
	n, err := zw.Close()
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("disk %s: %d bytes in the backup, %d expected", target, n, size)
	}
	return conn.Flush()
}

func strconv64(n int64) string { return fmt.Sprintf("%d", n) }

func restoreVMImage(ctx context.Context, t *store.BackupTask, it *Item, r GuestRestore, p Progress) (string, error) {
	if !sys.Has("qemu-nbd") {
		return "", fmt.Errorf("qemu-nbd is not installed")
	}
	if err := vms.ValidName(r.Name); err != nil {
		return "", err
	}
	dest := filepath.Clean(r.Storage)
	if !strings.HasPrefix(dest, storage.MountRoot+"/") {
		return "", fmt.Errorf("choose a folder of a volume for the VM")
	}
	if cur, err := vms.Get(r.Name); err == nil {
		if !r.Replace {
			return "", fmt.Errorf("a VM named %s already exists", r.Name)
		}
		if cur.State != "shut off" {
			return "", fmt.Errorf("shut %s down before replacing it", r.Name)
		}
	}
	b, err := readGuestBundle(ctx, t, it.ID)
	if err != nil {
		return "", err
	}
	parts := map[string]string{}
	for _, pt := range it.Parts {
		parts[pt.Name] = pt.ID
	}
	vol, err := stagingVolume(dest)
	if err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(vol, ".strata-imgrestore-")
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := vms.WriteBundle(stage, b); err != nil {
		return "", err
	}
	for i, d := range b.Disks {
		snap := parts["disk-"+d.Target]
		if snap == "" || !diskTargetRe.MatchString(d.Target) || d.Size <= 0 {
			return "", fmt.Errorf("disk %s is missing from the backup", d.Target)
		}
		p.Log("Restoring disk %s (%s)", d.Target, humanBytes(d.Size))
		from, to := float64(i)*90/float64(len(b.Disks)), float64(i+1)*90/float64(len(b.Disks))
		if err := writeDisk(ctx, t, snap, d.Target, filepath.Join(stage, d.Target+".qcow2"), d.Size,
			Progress{Log: p.Log, Set: func(v float64) { p.Set(from + (to-from)*v/100) }}); err != nil {
			return "", err
		}
	}
	if r.Replace {
		if _, err := vms.Get(r.Name); err == nil {
			p.Log("Deleting the current VM %s", r.Name)
			if err := vms.Delete(r.Name, true); err != nil {
				return "", err
			}
			keep = true // from here, the staged copy is the only one
		}
	}
	vp := vms.Progress{Log: p.Log, Set: func(v float64) { p.Set(90 + v/10) }}
	if err := vms.RestoreVM(ctx, stage, vms.RestoreReq{Name: r.Name, Storage: dest, Start: r.Start, MoveFiles: true}, vp); err != nil {
		if keep {
			p.Log("The restored disks are kept in %s", stage)
		}
		return "", err
	}
	keep = false
	return fmt.Sprintf("VM %s restored from the backup of %s", r.Name, it.Time.Local().Format("2006-01-02 15:04")), nil
}

func restoreContainerImage(ctx context.Context, t *store.BackupTask, it *Item, r GuestRestore, p Progress) (string, error) {
	if err := lxc.ValidName(r.Name); err != nil {
		return "", err
	}
	if lxc.Exists(r.Name) {
		if !r.Replace {
			return "", fmt.Errorf("a container named %s already exists", r.Name)
		}
		if lxc.State(r.Name) != "stopped" {
			return "", fmt.Errorf("stop %s before replacing it", r.Name)
		}
	}
	pref := r.Storage
	if pref == "" {
		if s, err := lxc.Load(r.Name); err == nil {
			pref = s.Storage.Location
		}
	}
	vol, err := stagingVolume(pref)
	if err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(vol, ".strata-imgrestore-")
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(stage)
		}
	}()
	archive := filepath.Join(stage, r.Name+".tar.zst")
	f, err := os.OpenFile(archive, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	p.Log("Reading the container from the backup (%s)", humanBytes(it.Logical))
	zstd := exec.CommandContext(ctx, "zstd", "-q", "-T0", "-3", "-c")
	zstd.Stdout = f
	in, err := zstd.StdinPipe()
	if err != nil {
		f.Close()
		return "", err
	}
	if err := zstd.Start(); err != nil {
		f.Close()
		return "", err
	}
	cr := &countingReader{size: it.Logical, set: func(v float64) { p.Set(v * 0.6) }}
	derr := dumpTo(ctx, t, it.ID, "/rootfs.tar", writerFunc(func(b []byte) (int, error) {
		cr.n += int64(len(b))
		if time.Since(cr.last) > time.Second {
			cr.last = time.Now()
			cr.set(min(100, float64(cr.n)*100/float64(max(cr.size, 1))))
		}
		return in.Write(b)
	}))
	in.Close()
	zerr := zstd.Wait()
	f.Close()
	if derr != nil {
		return "", derr
	}
	if zerr != nil {
		return "", fmt.Errorf("zstd: %v", zerr)
	}
	if r.Replace && lxc.Exists(r.Name) {
		p.Log("Deleting the current container %s", r.Name)
		if err := lxc.Delete(ctx, r.Name); err != nil {
			return "", err
		}
		keep = true
	}
	p.Set(65)
	err = lxc.Import(ctx, lxc.ImportReq{Source: archive, Name: r.Name, Location: r.Storage, Backend: r.Backend, Start: r.Start}, p.Log)
	if err != nil {
		if keep {
			p.Log("The container backup is kept in %s (Containers → Import)", archive)
		}
		return "", err
	}
	keep = false
	return fmt.Sprintf("Container %s restored from the backup of %s", r.Name, it.Time.Local().Format("2006-01-02 15:04")), nil
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }
