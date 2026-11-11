package backup

// Image backups of VMs: three ways to read the disks (see incremental.go).
//   - live: a running VM whose disks are all qcow2 files, in snapshot mode,
//     through libvirt's backup API; incremental from the task's previous
//     checkpoint when it is still valid;
//   - RBD: a VM whose disks are all on Ceph, from RBD snapshots, incremental
//     with rbd export-diff from the task's previous snapshot;
//   - classic: everything else (stopped VMs, suspend/shutdown modes, raw
//     files), read in full through qemu-nbd while the VM is held still.
// Incremental runs need the datastore on this NAS (DestType local): the
// unchanged blocks are read back from the previous backup, which would
// mean downloading it from a remote repository.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"strataos/internal/ceph"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/vms"
)

// RBDConnOf finds how to reach the Ceph cluster holding an RBD pool of VM
// disks (set by the api package).
var RBDConnOf func(pool string) (ceph.Conn, error)

// imageStats sums up a guest backup: logical size, new data stored, bytes
// read from the VM's storage, and whether a disk was read incrementally.
type imageStats struct {
	logical, added, read int64
	incremental          bool
}

var taskIDRe = regexp.MustCompile(`^[A-Za-z0-9]{1,40}$`)

// incrementalAllowed: the repository is on this NAS and the task id can
// name checkpoints and snapshots.
func incrementalAllowed(t *store.BackupTask) bool {
	return t.DestType == "local" && taskIDRe.MatchString(t.ID)
}

// prevImage is the last complete backup of a VM by the task on this NAS.
type prevImage struct {
	bundle *vms.Bundle
	parts  map[string]string // disk target → snapshot id
}

func (pi *prevImage) disk(target string) (vms.ImageDisk, string, bool) {
	if pi == nil {
		return vms.ImageDisk{}, "", false
	}
	for _, d := range pi.bundle.Disks {
		if d.Target == target && pi.parts[target] != "" {
			return d, pi.parts[target], true
		}
	}
	return vms.ImageDisk{}, "", false
}

func previousImage(ctx context.Context, t *store.BackupTask, name string) *prevImage {
	snaps, err := taskSnapshots(ctx, t, "--tag", guestTag+"vm-"+name)
	if err != nil {
		return nil
	}
	for _, it := range GroupSnapshots(snaps) {
		if it.Kind != "vm" || !it.Complete {
			continue
		}
		b, err := readGuestBundle(ctx, t, it.ID)
		if err != nil {
			return nil
		}
		pi := &prevImage{bundle: b, parts: map[string]string{}}
		for _, pt := range it.Parts {
			if tg, ok := strings.CutPrefix(pt.Name, "disk-"); ok {
				pi.parts[tg] = pt.ID
			}
		}
		return pi
	}
	return nil
}

// dumpPart reads a file of a snapshot (a variable for the tests).
var dumpPart = dumpTo

// patchedStream returns the stream of a disk rebuilt by apply from the
// previous backup of it (restic dump), and a function that waits for the
// end and returns the bytes apply read from the VM's storage.
func patchedStream(ctx context.Context, t *store.BackupTask, prevSnap, target string, apply func(w io.Writer, prev io.Reader) (int64, error)) (*io.PipeReader, func() (int64, error)) {
	pr, pw := io.Pipe()
	or, ow := io.Pipe()
	dumped := make(chan error, 1)
	go func() {
		err := dumpPart(ctx, t, prevSnap, "/"+target+".raw", pw)
		pw.CloseWithError(err)
		dumped <- err
	}()
	type result struct {
		n   int64
		err error
	}
	res := make(chan result, 1)
	go func() {
		n, err := apply(ow, pr)
		pr.CloseWithError(io.ErrClosedPipe) // stops the dump when apply ended early
		if err == nil {
			ow.Close()
		} else {
			ow.CloseWithError(err)
		}
		res <- result{n, err}
	}()
	return or, func() (int64, error) {
		r := <-res
		derr := <-dumped
		if r.err == nil && derr != nil && !errors.Is(derr, io.ErrClosedPipe) {
			r.err = derr
		}
		return r.n, r.err
	}
}

// backupVMImage stores the disks of a VM, then its definition.
func backupVMImage(ctx context.Context, t *store.BackupTask, name string, at time.Time, p Progress) (imageStats, error) {
	if !sys.Has("qemu-nbd") {
		return imageStats{}, fmt.Errorf("qemu-nbd is not installed")
	}
	p.Log("Backing up virtual machine %s", name)
	b, vm, err := vms.ImageBundle(name)
	if err != nil {
		return imageStats{}, err
	}
	rbd, files, live := 0, 0, true
	for _, d := range b.Disks {
		if strings.HasPrefix(d.Source, vms.RBDPrefix) {
			rbd++
		} else {
			files++
			live = live && vms.LiveBackupDisk(vms.Disk{Device: "disk", Format: d.Format, Source: d.Source})
		}
	}
	var prev *prevImage
	if incrementalAllowed(t) {
		prev = previousImage(ctx, t, name)
	}
	switch {
	case rbd > 0 && files > 0:
		return imageStats{}, fmt.Errorf("a VM with disks on Ceph and in files cannot be backed up as an image")
	case rbd > 0:
		return backupRBDVM(ctx, t, b, at, prev, p)
	case files > 0 && live && vm.State == "running" && (t.VMMode == "" || t.VMMode == vms.BackupSnapshot) &&
		taskIDRe.MatchString(t.ID) && vms.LiveBackupSupported():
		return backupVMLive(ctx, t, b, at, prev, p)
	}
	return backupVMClassic(ctx, t, name, at, prev, p)
}

// storeDisks backs up each disk with the reader open gives (and its
// closing), then the definition; parts of a failed run are forgotten.
func storeVM(ctx context.Context, t *store.BackupTask, b *vms.Bundle, at time.Time, p Progress,
	disk func(i int, d *vms.ImageDisk, p Progress) (io.Reader, func() (read int64, incr bool, err error), error), release func()) (st imageStats, err error) {
	guest := guestTag + "vm-" + b.Manifest.Name
	var ids []string
	defer func() {
		if err != nil {
			forgetIDs(ctx, t, ids)
		}
	}()
	for i := range b.Disks {
		d := &b.Disks[i]
		from, to := float64(i)*95/float64(len(b.Disks)), float64(i+1)*95/float64(len(b.Disks))
		dp := Progress{Log: p.Log, Set: func(v float64) { p.Set(from + (to-from)*v/100) }}
		r, finish, err := disk(i, d, dp)
		if err != nil {
			return st, fmt.Errorf("disk %s: %w", d.Target, err)
		}
		cr := &countingReader{r: r, size: d.Size, set: dp.Set}
		sum, err := backupStream(ctx, t, cr, d.Target+".raw", at, []string{guest, partTag + "disk-" + d.Target}, p)
		if c, ok := r.(io.Closer); ok {
			_ = c.Close()
		}
		read, incr, ferr := finish()
		if err == nil {
			err = ferr
		}
		if err == nil && cr.n != d.Size {
			err = fmt.Errorf("read %d of %d bytes", cr.n, d.Size)
		}
		if sum.SnapshotID != "" {
			ids = append(ids, sum.SnapshotID)
		}
		if err != nil {
			return st, fmt.Errorf("disk %s: %w", d.Target, err)
		}
		st.logical += sum.TotalBytes
		st.added += sum.DataAddedPacked
		st.read += read
		if incr {
			st.incremental = true
			p.Log("Disk %s: incremental: %s read, %s new data stored", d.Target, humanBytes(read), humanBytes(sum.DataAddedPacked))
		} else {
			p.Log("Disk %s: %s new data stored", d.Target, humanBytes(sum.DataAddedPacked))
		}
	}
	if release != nil {
		release()
	}
	b.Manifest.Size = st.logical
	data, err := json.Marshal(b)
	if err != nil {
		return st, err
	}
	sum, err := backupStream(ctx, t, bytes.NewReader(data), "guest.json", at, []string{guest, partTag + "config"}, p)
	if sum.SnapshotID != "" {
		ids = append(ids, sum.SnapshotID)
	}
	if err != nil {
		return st, err
	}
	st.added += sum.DataAddedPacked
	if st.incremental {
		p.Log("VM %s: incremental: %s read of %s", b.Manifest.Name, humanBytes(st.read), humanBytes(st.logical))
	}
	p.Set(100)
	return st, nil
}

// backupVMClassic reads every disk in full through qemu-nbd while the VM
// is held still. The task's last checkpoint stays the base of the next
// incremental run: everything that changed after it is in this copy too.
func backupVMClassic(ctx context.Context, t *store.BackupTask, name string, at time.Time, prev *prevImage, p Progress) (imageStats, error) {
	src, err := vms.OpenForImage(ctx, name, t.VMMode, vms.Progress{Log: p.Log})
	if err != nil {
		return imageStats{}, err
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
	if prev != nil && prev.bundle.Checkpoint != "" && vms.HasCheckpoint(name, prev.bundle.Checkpoint) {
		b.Checkpoint = prev.bundle.Checkpoint
	}
	var closeImg func()
	return storeVM(ctx, t, b, at, p, func(i int, d *vms.ImageDisk, p Progress) (io.Reader, func() (int64, bool, error), error) {
		conn, done, err := openImage(ctx, d.Source, d.Format, false)
		if err != nil {
			return nil, nil, err
		}
		closeImg = done
		d.Size = conn.size
		p.Log("Disk %s: %s (%s)", d.Target, humanBytes(conn.size), b.Manifest.Consistency)
		return &nbdReader{n: conn}, func() (int64, bool, error) { closeImg(); return conn.size, false, nil }, nil
	}, release)
}

// backupVMLive reads a running VM through a libvirt pull-mode backup.
func backupVMLive(ctx context.Context, t *store.BackupTask, b *vms.Bundle, at time.Time, prev *prevImage, p Progress) (st imageStats, err error) {
	name := b.Manifest.Name
	disks := map[string]string{}
	for _, d := range b.Disks {
		disks[d.Target] = d.Source
	}
	since := ""
	if prev != nil && prev.bundle.Checkpoint != "" && vms.HasCheckpoint(name, prev.bundle.Checkpoint) {
		since = prev.bundle.Checkpoint
	} else if prev != nil {
		p.Log("No valid checkpoint of the last backup (VM moved, restored or never backed up live): the disks are read in full")
	}
	cp := fmt.Sprintf("%s%d", vms.CheckpointPrefix(t.ID), at.Unix())
	vp := vms.Progress{Log: p.Log}
	lb, err := vms.BeginLiveBackup(name, disks, cp, since, vp)
	if err != nil && since != "" {
		// Bitmaps broken (QEMU killed, image changed elsewhere): start over.
		p.Log("Incremental backup impossible (%v): the disks are read in full", err)
		vms.DropCheckpoints(name, vms.CheckpointPrefix(t.ID), "")
		_ = vms.DeleteCheckpoint(name, cp)
		since = ""
		lb, err = vms.BeginLiveBackup(name, disks, cp, "", vp)
	}
	if err != nil {
		_ = vms.DeleteCheckpoint(name, cp)
		return st, err
	}
	b.Manifest.Consistency = lb.Consistency
	defer func() {
		lb.End()
		if err != nil {
			_ = vms.DeleteCheckpoint(name, cp)
		} else {
			vms.DropCheckpoints(name, vms.CheckpointPrefix(t.ID), cp)
		}
	}()
	b.Checkpoint = cp
	return storeVM(ctx, t, b, at, p, func(i int, d *vms.ImageDisk, p Progress) (io.Reader, func() (int64, bool, error), error) {
		conn, err := dialNBD(lb.Socket, d.Target, "")
		if err != nil {
			return nil, nil, err
		}
		d.Size = conn.size
		pd, snap, ok := prev.disk(d.Target)
		if since == "" || !ok || pd.Size != conn.size || pd.Source != d.Source {
			p.Log("Disk %s: %s, read in full (%s)", d.Target, humanBytes(conn.size), lb.Consistency)
			return &nbdReader{n: conn}, func() (int64, bool, error) { conn.Close(); return conn.size, false, nil }, nil
		}
		bm, err := dialNBD(lb.Socket, d.Target, lb.BitmapContext(d.Target))
		if err != nil {
			conn.Close()
			return nil, nil, err
		}
		ext, err := bm.dirtyExtents()
		bm.Close()
		if err != nil {
			conn.Close()
			return nil, nil, err
		}
		var changed int64
		for _, e := range ext {
			changed += e.Len
		}
		p.Log("Disk %s: %s, %s changed since the last backup (%s)", d.Target, humanBytes(conn.size), humanBytes(changed), lb.Consistency)
		r, wait := patchedStream(ctx, t, snap, d.Target, func(w io.Writer, prev io.Reader) (int64, error) {
			return mergeStream(w, prev, conn, conn.size, ext)
		})
		return r, func() (int64, bool, error) {
			n, err := wait()
			conn.Close()
			return n, true, err
		}, nil
	}, nil)
}

// rbdSnapRe: the RBD snapshots StrataOS makes.
var rbdSnapRe = regexp.MustCompile(`^strata-[A-Za-z0-9]{1,40}-[0-9]{1,20}$`)

// backupRBDVM reads a VM whose disks are on Ceph from RBD snapshots.
func backupRBDVM(ctx context.Context, t *store.BackupTask, b *vms.Bundle, at time.Time, prev *prevImage, p Progress) (st imageStats, err error) {
	if RBDConnOf == nil || !sys.Has("rbd") {
		return st, fmt.Errorf("the Ceph tools are not installed")
	}
	if !taskIDRe.MatchString(t.ID) {
		return st, fmt.Errorf("invalid task id")
	}
	type img struct {
		spec string // pool/image
		conn ceph.Conn
	}
	imgs := make([]img, len(b.Disks))
	for i, d := range b.Disks {
		spec := strings.TrimPrefix(d.Source, vms.RBDPrefix)
		pool, _, _ := strings.Cut(spec, "/")
		c, err := RBDConnOf(pool)
		if err != nil {
			return st, fmt.Errorf("disk %s: %w", d.Target, err)
		}
		imgs[i] = img{spec, c}
	}
	snap := fmt.Sprintf("%s%d", vms.CheckpointPrefix(t.ID), at.Unix())
	release, consistency, err := vms.HoldForSnapshots(b.Manifest.Name, t.VMMode, vms.Progress{Log: p.Log})
	if err != nil {
		return st, err
	}
	var made []int
	for i, im := range imgs {
		if _, err = im.conn.RBD(ctx, "snap", "create", im.spec+"@"+snap); err != nil {
			break
		}
		made = append(made, i)
	}
	release()
	keep := incrementalAllowed(t)
	defer func() {
		for _, i := range made {
			im := imgs[i]
			if err != nil || !keep {
				_, _ = im.conn.RBD(context.WithoutCancel(ctx), "snap", "rm", im.spec+"@"+snap)
				continue
			}
			// The new snapshot is the base of the next run: older ones go.
			for _, old := range rbdSnaps(ctx, im.conn, im.spec) {
				if old != snap && strings.HasPrefix(old, vms.CheckpointPrefix(t.ID)) {
					_, _ = im.conn.RBD(context.WithoutCancel(ctx), "snap", "rm", im.spec+"@"+old)
				}
			}
		}
	}()
	if err != nil {
		return st, fmt.Errorf("RBD snapshot: %w", err)
	}
	b.Manifest.Consistency = consistency
	return storeVM(ctx, t, b, at, p, func(i int, d *vms.ImageDisk, p Progress) (io.Reader, func() (int64, bool, error), error) {
		im := imgs[i]
		var info struct {
			Size int64 `json:"size"`
		}
		out, err := im.conn.RBD(ctx, "info", "--format", "json", im.spec+"@"+snap)
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(out), &info); err != nil || info.Size <= 0 {
			return nil, nil, fmt.Errorf("unexpected answer of rbd info")
		}
		d.Size, d.Snapshot, d.Format = info.Size, snap, "raw"
		pd, psnap, ok := prev.disk(d.Target)
		incr := keep && ok && pd.Size == info.Size && pd.Source == d.Source && rbdSnapRe.MatchString(pd.Snapshot) &&
			strings.HasPrefix(pd.Snapshot, vms.CheckpointPrefix(t.ID)) && hasSnap(rbdSnaps(ctx, im.conn, im.spec), pd.Snapshot)
		rp, wp := io.Pipe()
		exported := make(chan error, 1)
		args := []string{"export", "--no-progress", im.spec + "@" + snap, "-"}
		if incr {
			args = []string{"export-diff", "--no-progress", "--from-snap", pd.Snapshot, im.spec + "@" + snap, "-"}
		}
		go func() {
			err := sys.Stream(ctx, sys.Opts{Timeout: 48 * time.Hour}, wp, "rbd", im.conn.Args(args...)...)
			wp.CloseWithError(err)
			exported <- err
		}()
		if !incr {
			p.Log("Disk %s: %s on Ceph, read in full (%s)", d.Target, humanBytes(info.Size), consistency)
			return rp, func() (int64, bool, error) {
				rp.CloseWithError(io.ErrClosedPipe)
				if err := <-exported; err != nil && !errors.Is(err, io.ErrClosedPipe) {
					return 0, false, err
				}
				return info.Size, false, nil
			}, nil
		}
		p.Log("Disk %s: %s on Ceph, changes since %s (%s)", d.Target, humanBytes(info.Size), pd.Snapshot, consistency)
		r, wait := patchedStream(ctx, t, psnap, d.Target, func(w io.Writer, prev io.Reader) (int64, error) {
			di, err := applyRBDDiff(w, prev, info.Size, rp)
			rp.CloseWithError(io.ErrClosedPipe)
			return di.Read, err
		})
		return r, func() (int64, bool, error) {
			n, err := wait()
			if xerr := <-exported; err == nil && xerr != nil && !errors.Is(xerr, io.ErrClosedPipe) {
				err = xerr
			}
			return n, true, err
		}, nil
	}, nil)
}

func rbdSnaps(ctx context.Context, c ceph.Conn, spec string) []string {
	out, err := c.RBD(ctx, "snap", "ls", "--format", "json", spec)
	if err != nil {
		return nil
	}
	var list []struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal([]byte(out), &list)
	var names []string
	for _, s := range list {
		names = append(names, s.Name)
	}
	return names
}

func hasSnap(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
