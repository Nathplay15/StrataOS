package vms

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"strataos/internal/sys"
)

// CloudImage is an official cloud image (cloud-init inside) VMs can be made from.
type CloudImage struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Base   string `json:"-"` // folder URL
	File   string `json:"file"`
	Sums   string `json:"-"` // checksum file in Base
	Algo   string `json:"algo"`
	Cached bool   `json:"cached"`
}

func cloudImages() []CloudImage {
	arch := runtime.GOARCH
	return []CloudImage{
		{ID: "debian13", Name: "Debian 13 (trixie)", Base: "https://cloud.debian.org/images/cloud/trixie/latest/",
			File: "debian-13-genericcloud-" + arch + ".qcow2", Sums: "SHA512SUMS", Algo: "sha512"},
		{ID: "ubuntu2404", Name: "Ubuntu 24.04 LTS", Base: "https://cloud-images.ubuntu.com/releases/noble/release/",
			File: "ubuntu-24.04-server-cloudimg-" + arch + ".img", Sums: "SHA256SUMS", Algo: "sha256"},
	}
}

func cloudImage(id string) (CloudImage, error) {
	for _, c := range cloudImages() {
		if c.ID == id {
			return c, nil
		}
	}
	return CloudImage{}, fmt.Errorf("unknown cloud image %q", id)
}

// CloudImages lists the catalog; Cached tells whether a volume already holds it.
func CloudImages() []CloudImage {
	list := cloudImages()
	for i, c := range list {
		if m, _ := filepath.Glob(filepath.Join("/srv/strata", "*", cacheDirName, c.File)); len(m) > 0 {
			list[i].Cached = true
		}
	}
	return list
}

const cacheDirName = ".strata-cloud-images"

// VolumeRoot returns /srv/strata/<volume> for a path inside it.
func VolumeRoot(p string) string {
	rel := strings.TrimPrefix(filepath.Clean(p), "/srv/strata/")
	first, _, _ := strings.Cut(rel, "/")
	return filepath.Join("/srv/strata", first)
}

var hexRe = regexp.MustCompile(`^[0-9a-fA-F]{64,128}$`)

// parseChecksums reads a SHA*SUMS file ("<hash>  <file>" or "<hash> *<file>").
func parseChecksums(r io.Reader) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 || !hexRe.MatchString(f[0]) {
			continue
		}
		out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
	}
	return out
}

func newHash(algo string) hash.Hash {
	if algo == "sha512" {
		return sha512.New()
	}
	return sha256.New()
}

// fileHash returns the hex digest of a file.
func fileHash(path, algo string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := newHash(algo)
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyChecksum compares a file against its expected digest.
func verifyChecksum(path, algo, want string) error {
	got, err := fileHash(path, algo)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: the download is corrupt or was tampered with", filepath.Base(path))
	}
	return nil
}

var httpClient = &http.Client{Transport: http.DefaultTransport}

func httpGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return resp, nil
}

// EnsureImage downloads a cloud image once into the cache folder of a volume,
// verified against the official checksum file, and returns its path.
func EnsureImage(ctx context.Context, id, cacheDir string, p Progress) (string, error) {
	img, err := cloudImage(id)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(cacheDir, img.File)
	marker := path + ".verified" // digest of the cached file, checked when it was downloaded
	sctx, cancel := context.WithTimeout(ctx, time.Minute)
	resp, err := httpGet(sctx, img.Base+img.Sums)
	var want string
	if err == nil {
		want = parseChecksums(io.LimitReader(resp.Body, 1<<20))[img.File]
		resp.Body.Close()
	}
	cancel()
	if want == "" {
		// Offline: a copy verified earlier is still trusted.
		if old, rerr := os.ReadFile(marker); rerr == nil {
			if verifyChecksum(path, img.Algo, strings.TrimSpace(string(old))) == nil {
				p.log("Checksums unreachable (%v): using the cached image verified earlier", err)
				return path, nil
			}
		}
		if err == nil {
			err = fmt.Errorf("%s not listed in %s", img.File, img.Sums)
		}
		return "", fmt.Errorf("cannot get the official checksums: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		p.log("Checking the cached %s", img.File)
		if verifyChecksum(path, img.Algo, want) == nil {
			_ = os.WriteFile(marker, []byte(want+"\n"), 0o644)
			p.set(100)
			return path, nil
		}
		p.log("The cached image is outdated or damaged: downloading it again")
	}
	p.log("Downloading %s", img.Base+img.File)
	resp, err = httpGet(ctx, img.Base+img.File)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	part := path + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	h := newHash(img.Algo)
	total := resp.ContentLength
	var done int64
	buf := make([]byte, 1<<20)
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				_ = os.Remove(part)
				return "", werr
			}
			h.Write(buf[:n])
			done += int64(n)
			if total > 0 && time.Since(last) > time.Second {
				p.set(float64(done) * 100 / float64(total))
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			_ = os.Remove(part)
			return "", rerr
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		_ = os.Remove(part)
		return "", fmt.Errorf("checksum mismatch for %s: the download is corrupt or was tampered with", img.File)
	}
	if err := os.Rename(part, path); err != nil {
		return "", err
	}
	_ = os.WriteFile(marker, []byte(want+"\n"), 0o644)
	p.log("%s verified (%s)", img.File, strings.ToUpper(img.Algo))
	p.set(100)
	return path, nil
}

// CloudReq creates a VM from a cloud image.
type CloudReq struct {
	CreateReq
	Image string    `json:"image"`
	Cloud CloudInit `json:"cloudInit"`
}

// CreateFromCloud copies a cached cloud image into a new VM, grows its disk,
// attaches a cloud-init seed and boots it: it is reachable over SSH a minute later.
func CreateFromCloud(ctx context.Context, r CloudReq, p Progress) error {
	img, err := cloudImage(r.Image)
	if err != nil {
		return err
	}
	if r.Cloud.Hostname == "" {
		r.Cloud.Hostname = strings.ToLower(strings.NewReplacer("_", "-", ".", "-").Replace(r.Name))
	}
	if err := r.Cloud.Validate(); err != nil {
		return err
	}
	r.OS = "linux"
	if !sys.Has("xorriso") && !sys.Has("genisoimage") && !sys.Has("cloud-localds") {
		// Installed with the virtualisation component since 0.6.
		p.log("Installing xorriso (cloud-init seed images)")
		if _, err := sys.RunOpts(ctx, sys.Opts{Timeout: 10 * time.Minute, Env: []string{"DEBIAN_FRONTEND=noninteractive"}}, "apt-get", "install", "-y", "--no-install-recommends", "xorriso"); err != nil {
			return err
		}
	}
	dir, err := newVMDir(&r.CreateReq)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	src, err := EnsureImage(ctx, img.ID, filepath.Join(VolumeRoot(dir), cacheDirName), p.scaled(0, 70))
	if err != nil {
		return err
	}
	disk := filepath.Join(dir, "disk0.qcow2")
	if err := convertImage(ctx, src, "qcow2", disk, false, p.scaled(70, 90)); err != nil {
		return err
	}
	if err := resizeImage(disk, r.DiskGB); err != nil {
		return err
	}
	seed, err := writeSeed(dir, r.Name, r.Cloud)
	if err != nil {
		return err
	}
	spec := installSpec{import_: true,
		disks: []string{"path=" + disk + ",format=qcow2,bus=virtio,cache=none,discard=unmap", "path=" + seed + ",device=cdrom,readonly=on"},
		// The serial console is logged: cloud-init prints its progress there.
		extra: []string{"--serial", "pty,log.file=" + filepath.Join(dir, "console.log")},
	}
	if err := install(ctx, r.CreateReq, spec, p.Log); err != nil {
		return err
	}
	ok = true
	_ = writeMeta(r.Name, strataMeta{Cloud: img.ID})
	p.set(100)
	return nil
}

// ConsoleLog returns the end of the serial console log of a VM (cloud images, imports).
func ConsoleLog(name string) (string, error) {
	vm, err := Get(name)
	if err != nil {
		return "", err
	}
	if vm.Dir == "" {
		return "", nil
	}
	f, err := os.Open(filepath.Join(vm.Dir, "console.log"))
	if err != nil {
		return "", nil
	}
	defer f.Close()
	const max = 64 << 10
	if fi, err := f.Stat(); err == nil && fi.Size() > max {
		_, _ = f.Seek(fi.Size()-max, io.SeekStart)
	}
	b, _ := io.ReadAll(io.LimitReader(f, max))
	return string(b), nil
}
