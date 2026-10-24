package ceph

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"strataos/internal/sys"
)

// CephFSSource is the fstab source of a CephFS mount (mount.ceph syntax).
func CephFSSource(user, fsid, fs, path string) string {
	return fmt.Sprintf("%s@%s.%s=%s", user, fsid, fs, path)
}

// CephFSOptions are the mount options of a CephFS volume.
func CephFSOptions(secretFile string, mons []string) string {
	return "secretfile=" + secretFile + ",mon_addr=" + MountMonAddr(mons) + ",noatime,_netdev,noauto,nofail"
}

// EnsureFSPath creates a folder inside a CephFS filesystem before it is
// mounted on its own, by mounting the root for a moment.
func EnsureFSPath(ctx context.Context, user, fsid, fs, sub, secretFile string, mons []string) error {
	if sub == "/" {
		return nil
	}
	tmp, err := os.MkdirTemp("", "strata-cephfs")
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	opts := "secretfile=" + secretFile + ",mon_addr=" + MountMonAddr(mons)
	if _, err := sys.RunOpts(ctx, sys.Opts{Timeout: 2 * time.Minute}, "mount", "-t", "ceph", CephFSSource(user, fsid, fs, "/"), tmp, "-o", opts); err != nil {
		return err
	}
	defer func() { _, _ = sys.Run("umount", tmp) }()
	return os.MkdirAll(filepath.Join(tmp, sub), 0o755)
}

// Mapped is an RBD image mapped on this NAS.
type Mapped struct {
	ID     string
	Pool   string
	Image  string
	FSID   string
	Device string
}

// MappedImages lists the RBD images mapped by the kernel (from sysfs, which
// also tells the cluster of each one).
func MappedImages() []Mapped {
	return mappedIn("/sys/bus/rbd/devices")
}

func mappedIn(root string) []Mapped {
	var out []Mapped
	entries, _ := os.ReadDir(root)
	read := func(dir, f string) string {
		b, _ := os.ReadFile(filepath.Join(root, dir, f))
		return strings.TrimSpace(string(b))
	}
	for _, e := range entries {
		id := e.Name()
		if _, err := strconv.Atoi(id); err != nil {
			continue
		}
		out = append(out, Mapped{ID: id, Pool: read(id, "pool"), Image: read(id, "name"), FSID: read(id, "cluster_fsid"), Device: "/dev/rbd" + id})
	}
	return out
}

// FindMapped returns the device of a mapped image ("" when not mapped).
func FindMapped(fsid, pool, image string) string {
	for _, m := range MappedImages() {
		if m.Pool == pool && m.Image == image && (fsid == "" || m.FSID == fsid) {
			return m.Device
		}
	}
	return ""
}

// Watchers counts the clients that have an image open (another NAS that
// mapped it, a VM…).
func (c Conn) Watchers(ctx context.Context, pool, image string) (int, error) {
	out, err := c.RBD(ctx, "status", pool+"/"+image, "--format", "json")
	if err != nil {
		return 0, err
	}
	var st struct {
		Watchers []json.RawMessage `json:"watchers"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return 0, err
	}
	return len(st.Watchers), nil
}

// Map maps an image as a block device and returns it. It refuses an image
// another client has open: a filesystem must have a single writer.
func (c Conn) Map(ctx context.Context, fsid, pool, image string) (string, error) {
	if dev := FindMapped(fsid, pool, image); dev != "" {
		return dev, nil
	}
	if n, err := c.Watchers(ctx, pool, image); err != nil {
		return "", err
	} else if n > 0 {
		return "", fmt.Errorf("the image %s/%s is in use by another client: a filesystem on it can only be used by one NAS at a time", pool, image)
	}
	out, err := c.RBD(ctx, "device", "map", pool+"/"+image)
	if err != nil {
		return "", err
	}
	dev := strings.TrimSpace(out)
	if !devPathRe.MatchString(dev) {
		return "", fmt.Errorf("unexpected answer of rbd map: %q", dev)
	}
	_, _ = sys.Run("udevadm", "settle")
	return dev, nil
}

// Unmap releases a mapped image.
func Unmap(fsid, pool, image string) error {
	dev := FindMapped(fsid, pool, image)
	if dev == "" {
		return nil
	}
	_, err := sys.Run("rbd", "device", "unmap", dev)
	return err
}

// CreateImage creates an RBD image of sizeGB.
func (c Conn) CreateImage(ctx context.Context, pool, image string, sizeGB int) error {
	if sizeGB < 1 || sizeGB > 1<<20 {
		return fmt.Errorf("invalid size")
	}
	_, err := c.RBD(ctx, "create", pool+"/"+image, "--size", strconv.Itoa(sizeGB)+"G", "--image-feature", "layering,exclusive-lock,object-map,fast-diff,deep-flatten")
	return err
}

// RemoveImage deletes an RBD image.
func (c Conn) RemoveImage(ctx context.Context, pool, image string) error {
	_, err := c.RBD(ctx, "rm", "--no-progress", pool+"/"+image)
	return err
}

// Image is an RBD image of a pool.
type Image struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Images lists the images of a pool.
func (c Conn) Images(ctx context.Context, pool string) ([]Image, error) {
	out, err := c.RBD(ctx, "ls", "-l", "--format", "json", pool)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Image    string `json:"image"`
		Size     int64  `json:"size"`
		Snapshot string `json:"snapshot"`
	}
	if strings.TrimSpace(out) == "" {
		return []Image{}, nil
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, err
	}
	list := []Image{}
	for _, r := range raw {
		if r.Snapshot == "" {
			list = append(list, Image{Name: r.Image, Size: r.Size})
		}
	}
	return list, nil
}

// Pools lists the pools and filesystems a client can see.
func (c Conn) Pools(ctx context.Context) ([]Pool, []FS, error) {
	var raw []byte
	if err := c.JSON(ctx, &raw, "osd", "pool", "ls", "detail"); err != nil {
		return nil, nil, err
	}
	pools, err := ParsePools(raw)
	if err != nil {
		return nil, nil, err
	}
	if c.JSON(ctx, &raw, "df") == nil {
		_ = MergeDF(pools, raw)
	}
	fss := []FS{}
	if c.JSON(ctx, &raw, "fs", "ls") == nil {
		fss, _ = ParseFSList(raw)
	}
	return pools, fss, nil
}

// ClientCaps are the rights of a client key made for another NAS: an RBD
// pool and/or a CephFS filesystem.
func ClientCaps(pool, fs string) ([]string, error) {
	if pool == "" && fs == "" {
		return nil, fmt.Errorf("choose a pool or a filesystem")
	}
	var mon, osd, mds, mgr []string
	if pool != "" {
		if err := ValidPool(pool); err != nil {
			return nil, err
		}
		mon = append(mon, "profile rbd")
		osd = append(osd, "profile rbd pool="+pool)
		mgr = append(mgr, "profile rbd pool="+pool)
	}
	if fs != "" {
		if err := ValidPool(fs); err != nil {
			return nil, err
		}
		mon = append(mon, "allow r fsname="+fs)
		osd = append(osd, "allow rw tag cephfs data="+fs)
		mds = append(mds, "allow rw fsname="+fs)
	}
	caps := []string{"mon", strings.Join(mon, ", "), "osd", strings.Join(osd, ", ")}
	if len(mds) > 0 {
		caps = append(caps, "mds", strings.Join(mds, ", "))
	}
	if len(mgr) > 0 {
		caps = append(caps, "mgr", strings.Join(mgr, ", "))
	}
	return caps, nil
}

// LibvirtSecretXML defines the libvirt secret holding a cephx key.
func LibvirtSecretXML(uuid, usage string) string {
	return fmt.Sprintf("<secret ephemeral='no' private='yes'>\n  <uuid>%s</uuid>\n  <usage type='ceph'>\n    <name>%s</name>\n  </usage>\n</secret>\n", uuid, xmlEscape(usage))
}

// LibvirtPoolXML defines a libvirt storage pool on an RBD pool.
func LibvirtPoolXML(name, pool, user, secretUUID string, mons []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<pool type='rbd'>\n  <name>%s</name>\n  <source>\n    <name>%s</name>\n", xmlEscape(name), xmlEscape(pool))
	for _, m := range mons {
		host, port := m, ""
		if i := strings.LastIndex(m, ":"); i > 0 && !strings.HasSuffix(m, "]") {
			host, port = m[:i], m[i+1:]
		}
		host = strings.Trim(host, "[]")
		if port != "" {
			fmt.Fprintf(&b, "    <host name='%s' port='%s'/>\n", xmlEscape(host), xmlEscape(port))
		} else {
			fmt.Fprintf(&b, "    <host name='%s'/>\n", xmlEscape(host))
		}
	}
	fmt.Fprintf(&b, "    <auth type='ceph' username='%s'>\n      <secret uuid='%s'/>\n    </auth>\n  </source>\n</pool>\n", xmlEscape(user), secretUUID)
	return b.String()
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "'", "&apos;", `"`, "&quot;").Replace(s)
}
