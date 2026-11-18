package vms

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MacOSCacheImage is an image the macOS VMs share, kept in the cache folder
// of a volume (cacheDirName): the OpenCore boot disk and Apple's recovery
// images (BaseSystem), each several hundred MB.
type MacOSCacheImage struct {
	Path    string   `json:"path"`
	Volume  string   `json:"volume"`
	Kind    string   `json:"kind"`              // opencore | recovery
	Version string   `json:"version,omitempty"` // macOS release of a recovery image
	Size    int64    `json:"size"`
	UsedBy  []string `json:"usedBy,omitempty"` // VMs whose disks read it
}

// macosCachePatterns are the files EnsureOpenCore and EnsureBaseSystem
// write (the .dmg is the download, converted to the .img).
var macosCachePatterns = []string{"OpenCore-*.qcow2", "BaseSystem-*.img", "BaseSystem-*.dmg", "BaseSystem-*.chunklist"}

// macosCacheFiles lists the macOS images of every volume's cache folder.
func macosCacheFiles(root string) []string {
	var out []string
	for _, p := range macosCachePatterns {
		m, _ := filepath.Glob(filepath.Join(root, "*", cacheDirName, p))
		out = append(out, m...)
	}
	sort.Strings(out)
	return out
}

// chainOf follows the backing files of a disk (overlays of snapshots
// included), 32 levels at most.
func chainOf(path string, backing func(string) string) []string {
	var out []string
	for i := 0; i < 32; i++ {
		b := backing(path)
		if b == "" {
			break
		}
		out = append(out, b)
		path = b
	}
	return out
}

func macosCacheImages(root string, list []VM, backing func(string) string) []MacOSCacheImage {
	files := macosCacheFiles(root)
	used := map[string][]string{}
	for _, v := range list {
		for _, d := range v.Disks {
			if d.Device != "disk" || d.Source == "" || strings.HasPrefix(d.Source, "rbd:") {
				continue
			}
			for _, b := range chainOf(d.Source, backing) {
				if strings.Contains(b, "/"+cacheDirName+"/") && !contains(used[b], v.Name) {
					used[b] = append(used[b], v.Name)
				}
			}
		}
	}
	out := []MacOSCacheImage{}
	for _, f := range files {
		im := MacOSCacheImage{Path: f, Volume: filepath.Base(filepath.Dir(filepath.Dir(f))), Kind: "recovery", UsedBy: used[f]}
		base := filepath.Base(f)
		if strings.HasPrefix(base, "OpenCore-") {
			im.Kind = "opencore"
		} else {
			v := strings.TrimPrefix(base, "BaseSystem-")
			im.Version = strings.TrimSuffix(v, filepath.Ext(v))
			// The download and its chunk list are in use while the image is.
			if img := strings.TrimSuffix(f, filepath.Ext(f)) + ".img"; img != f {
				im.UsedBy = used[img]
			}
		}
		if fi, err := os.Stat(f); err == nil {
			im.Size = fi.Size()
		}
		out = append(out, im)
	}
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// MacOSCache lists the shared macOS images and the VMs that use them.
func MacOSCache() ([]MacOSCacheImage, error) {
	var list []VM
	if Installed() {
		var err error
		if list, err = List(); err != nil {
			return nil, err
		}
	}
	return macosCacheImages("/srv/strata", list, backingOf), nil
}

// DeleteMacOSCache removes the shared macOS images no VM uses (a download
// in progress is waited for first, and the list is read again then).
// It returns the files removed and the bytes freed.
func DeleteMacOSCache(ctx context.Context) ([]string, int64, error) {
	imgs, err := MacOSCache()
	if err != nil {
		return nil, 0, err
	}
	var removed []string
	var freed int64
	for _, im := range imgs {
		if len(im.UsedBy) > 0 {
			continue
		}
		key := im.Path
		if im.Kind == "recovery" {
			key = strings.TrimSuffix(im.Path, filepath.Ext(im.Path)) + ".img"
		}
		unlock, err := lockDownload(ctx, key)
		if err != nil {
			return removed, freed, err
		}
		// A VM created while waiting for the lock may use it now.
		again, _ := MacOSCache()
		busy := false
		for _, x := range again {
			if x.Path == im.Path && len(x.UsedBy) > 0 {
				busy = true
			}
		}
		if !busy {
			if err := os.Remove(im.Path); err != nil && !os.IsNotExist(err) {
				unlock()
				return removed, freed, fmt.Errorf("%s: %v", im.Path, err)
			}
			removed = append(removed, im.Path)
			freed += im.Size
		}
		unlock()
	}
	return removed, freed, nil
}

// InvariantTSC reports whether this machine's processor has the invariant
// TSC macOS needs (missing when StrataOS itself runs in a VM without it).
func InvariantTSC() bool { return invariantTSC(cpuinfoPath) }
