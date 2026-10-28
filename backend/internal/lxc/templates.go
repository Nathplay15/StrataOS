package lxc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/sys"
)

// Images come from images.linuxcontainers.org through lxc's "download"
// template, which checks the GPG signature of every image it downloads. The
// index (one line per image: distro;release;arch;variant;build;path) is
// fetched for the picker and cached for a few hours.

var (
	ImageServer = "images.linuxcontainers.org"
	indexMu     sync.Mutex
)

const indexTTL = 6 * time.Hour

// ImageInfo is one image the download template can install.
type ImageInfo struct {
	Distro      string `json:"distro"`
	Release     string `json:"release"`
	Arch        string `json:"arch"`
	Variant     string `json:"variant"`
	Build       string `json:"build"`
	Label       string `json:"label"`
	Recommended bool   `json:"recommended,omitempty"`
}

var imgFieldRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._\-]{0,40}$`)

// HostArch is the architecture name of the images for this NAS.
func HostArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	case "arm":
		return "armhf"
	}
	return runtime.GOARCH
}

// distroOrder puts the common distributions first in the picker.
var distroOrder = []string{"debian", "ubuntu", "alpine", "fedora", "archlinux", "rockylinux", "almalinux", "centos", "opensuse", "devuan", "oracle", "gentoo"}

var distroNames = map[string]string{
	"debian": "Debian", "ubuntu": "Ubuntu", "alpine": "Alpine", "fedora": "Fedora", "archlinux": "Arch Linux",
	"rockylinux": "Rocky Linux", "almalinux": "AlmaLinux", "centos": "CentOS", "opensuse": "openSUSE", "devuan": "Devuan",
	"oracle": "Oracle Linux", "gentoo": "Gentoo", "kali": "Kali", "voidlinux": "Void Linux", "nixos": "NixOS",
	"amazonlinux": "Amazon Linux", "openwrt": "OpenWrt", "mint": "Linux Mint", "busybox": "BusyBox", "plamo": "Plamo", "alt": "ALT",
	"funtoo": "Funtoo", "openeuler": "openEuler", "slackware": "Slackware",
}

var releaseNames = map[string]string{
	"debian/bookworm": "12 (bookworm)", "debian/trixie": "13 (trixie)", "debian/bullseye": "11 (bullseye)", "debian/forky": "14 (forky, testing)",
	"ubuntu/noble": "24.04 LTS (noble)", "ubuntu/jammy": "22.04 LTS (jammy)", "ubuntu/focal": "20.04 LTS (focal)", "ubuntu/resolute": "26.04 LTS (resolute)",
}

// recommended are offered first: the releases most people want.
var recommended = []string{"debian/trixie", "debian/bookworm", "ubuntu/noble", "ubuntu/jammy", "ubuntu/resolute", "archlinux/current"}

func imageLabel(distro, release string) string {
	d := distroNames[distro]
	if d == "" {
		d = strings.ToUpper(distro[:1]) + distro[1:]
	}
	r := releaseNames[distro+"/"+release]
	if r == "" {
		r = release
	}
	if release == "current" {
		return d
	}
	return d + " " + r
}

// ParseIndex reads the image index for one architecture (default variant).
func ParseIndex(content, arch string) []ImageInfo {
	var out []ImageInfo
	for _, l := range strings.Split(content, "\n") {
		f := strings.Split(strings.TrimSpace(l), ";")
		if len(f) < 5 || f[2] != arch || f[3] != "default" {
			continue
		}
		ok := true
		for _, v := range f[:4] {
			ok = ok && imgFieldRe.MatchString(v)
		}
		if !ok {
			continue
		}
		key := f[0] + "/" + f[1]
		out = append(out, ImageInfo{Distro: f[0], Release: f[1], Arch: f[2], Variant: f[3], Build: strings.TrimSpace(f[4]),
			Label: imageLabel(f[0], f[1]), Recommended: slices.Contains(recommended, key)})
	}
	rank := func(d string) int {
		if i := slices.Index(distroOrder, d); i >= 0 {
			return i
		}
		return len(distroOrder)
	}
	slices.SortStableFunc(out, func(a, b ImageInfo) int {
		if a.Recommended != b.Recommended {
			if a.Recommended {
				return -1
			}
			return 1
		}
		if a.Recommended {
			return slices.Index(recommended, a.Distro+"/"+a.Release) - slices.Index(recommended, b.Distro+"/"+b.Release)
		}
		if ra, rb := rank(a.Distro), rank(b.Distro); ra != rb {
			return ra - rb
		}
		if a.Distro != b.Distro {
			return strings.Compare(a.Distro, b.Distro)
		}
		// Newest releases first: compare the labels, numbers sort well enough.
		return strings.Compare(b.Label, a.Label)
	})
	return out
}

func indexPath() string { return filepath.Join(CacheDir, "index-system") }

// Images returns the image list, from the cache when it is fresh enough.
func Images(ctx context.Context, refresh bool) ([]ImageInfo, time.Time, error) {
	indexMu.Lock()
	defer indexMu.Unlock()
	fi, err := os.Stat(indexPath())
	if err != nil || refresh || time.Since(fi.ModTime()) > indexTTL {
		if ferr := fetchIndex(ctx); ferr != nil {
			if err != nil {
				return nil, time.Time{}, ferr
			}
			// Keep using the old copy when the server cannot be reached.
		}
		fi, err = os.Stat(indexPath())
		if err != nil {
			return nil, time.Time{}, err
		}
	}
	b, err := os.ReadFile(indexPath())
	if err != nil {
		return nil, time.Time{}, err
	}
	return ParseIndex(string(b), HostArch()), fi.ModTime(), nil
}

func fetchIndex(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://"+ImageServer+"/meta/1.0/index-system", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("image index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("image index: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(ParseIndex(string(b), HostArch())) == 0 {
		return fmt.Errorf("image index: no image for %s", HostArch())
	}
	if err := os.MkdirAll(CacheDir, 0o755); err != nil {
		return err
	}
	return sys.WriteFileAtomic(indexPath(), b, 0o644)
}

// findImage checks that an image is in the index (when the index is known).
func findImage(ctx context.Context, img Image) (Image, error) {
	for _, v := range []string{img.Distro, img.Release} {
		if !imgFieldRe.MatchString(v) {
			return img, fmt.Errorf("choose an image")
		}
	}
	img.Arch = HostArch()
	if img.Variant == "" {
		img.Variant = "default"
	}
	list, _, err := Images(ctx, false)
	if err != nil {
		return img, err
	}
	for _, i := range list {
		if i.Distro == img.Distro && i.Release == img.Release && i.Variant == img.Variant {
			img.Build = i.Build
			return img, nil
		}
	}
	return img, fmt.Errorf("no %s %s image for %s", img.Distro, img.Release, img.Arch)
}
