// Package lxc manages LXC system containers with the Debian lxc tools
// (lxc-create, lxc-start, lxc-attach…), next to the virtual machines.
//
// StrataOS describes each container with a Spec (container.json), the single
// source of its configuration: the lxc config file is generated from it. The
// spec is written next to the root file system on the volume
// (<location>/.strata-lxc/<name>/container.json) and copied into the lxc path
// (/var/lib/strata/lxc/<name>/), so that a container folder can be adopted
// again on another NAS or after a reinstall. Containers run in their own
// systemd unit (strata-lxc@<name>.service): restarting or updating StrataOS
// does not touch them.
package lxc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"strataos/internal/sys"
)

// LXCPath holds the lxc configuration of the containers. It is under
// /var/lib/strata, which restore points leave alone.
var LXCPath = "/var/lib/strata/lxc"

// CacheDir holds the image index and the generated seccomp profiles.
var CacheDir = "/var/lib/strata/lxc-cache"

// MountRoot is where StrataOS volumes are mounted.
const MountRoot = "/srv/strata"

// Installed reports whether the lxc tools are available.
func Installed() bool { return sys.Has("lxc-create") && sys.Has("lxc-start") && sys.Has("lxc-attach") }

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// reserved names would collide with the routes under /api/lxc/.
var reserved = []string{"templates", "storage", "archives", "import", "autostart", "shares", "users"}

// ValidName accepts host names: lower-case letters, digits and hyphens. The
// name is also the host name of the container, a systemd instance name and a
// ZFS dataset component, so nothing else is allowed.
func ValidName(n string) error {
	if !nameRe.MatchString(n) || slices.Contains(reserved, n) {
		return fmt.Errorf("invalid container name (lower-case letters, digits and hyphens, up to 63)")
	}
	return nil
}

// Spec is the StrataOS description of a container (container.json).
type Spec struct {
	Version     int       `json:"version"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Created     time.Time `json:"created"`
	Image       Image     `json:"image"`
	// Privileged containers run with the host's root as their root: only for
	// software that cannot run otherwise.
	Privileged bool        `json:"privileged,omitempty"`
	IDMap      IDMap       `json:"idmap"`
	Storage    StorageSpec `json:"storage"`
	// CPUs: CPU time limit in cores (0.5 = half a core; 0 = no limit).
	CPUs float64 `json:"cpus,omitempty"`
	// MemoryMB: memory limit (0 = no limit). SwapMB: swap allowed on top of it.
	MemoryMB int      `json:"memoryMb,omitempty"`
	SwapMB   int      `json:"swapMb,omitempty"`
	Network  Network  `json:"network"`
	Mounts   []Mount  `json:"mounts,omitempty"`
	Features Features `json:"features"`
	// Autostart: started with the NAS, by order, waiting Delay seconds after it.
	Autostart      bool `json:"autostart,omitempty"`
	AutostartOrder int  `json:"autostartOrder,omitempty"`
	AutostartDelay int  `json:"autostartDelay,omitempty"`
	// Includes and Arch come from the image's own configuration.
	Includes []string `json:"includes,omitempty"`
	Arch     string   `json:"arch,omitempty"`
}

// Image is the image the container was created from.
type Image struct {
	Distro  string `json:"distro"`
	Release string `json:"release"`
	Arch    string `json:"arch"`
	Variant string `json:"variant,omitempty"`
	Build   string `json:"build,omitempty"`
}

// Network is the single network interface (eth0) of a container.
type Network struct {
	Mode    string   `json:"mode"`             // nat | bridge | none
	Bridge  string   `json:"bridge,omitempty"` // host bridge (mode bridge)
	MAC     string   `json:"mac"`
	Address string   `json:"address,omitempty"` // static IPv4 in CIDR form; empty: DHCP
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
}

// Mount is a StrataOS shared folder mounted into the container.
type Mount struct {
	Share    string `json:"share"`  // name of the shared folder
	Source   string `json:"source"` // its path on the NAS
	Target   string `json:"target"` // absolute path inside the container
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// Features are optional kernel facilities.
type Features struct {
	Nesting bool `json:"nesting,omitempty"` // containers inside (Docker)
	FUSE    bool `json:"fuse,omitempty"`
	TUN     bool `json:"tun,omitempty"` // VPN software
	Keyctl  bool `json:"keyctl,omitempty"`
}

const specFile = "container.json"

// Dir is the lxc folder of a container (config, spec copy, logs).
func Dir(name string) string { return filepath.Join(LXCPath, name) }

// Exists reports whether a container is defined.
func Exists(name string) bool {
	_, err := os.Stat(filepath.Join(Dir(name), "config"))
	return err == nil
}

// Load reads the spec of a container.
func Load(name string) (*Spec, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(Dir(name), specFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("container %s not found", name)
		}
		return nil, err
	}
	return parseSpec(b)
}

func parseSpec(b []byte) (*Spec, error) {
	var s Spec
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("container.json: %w", err)
	}
	if err := ValidName(s.Name); err != nil {
		return nil, err
	}
	return &s, nil
}

// save writes the spec into the lxc folder and next to the root file system.
func save(s *Spec) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(Dir(s.Name), 0o750); err != nil {
		return err
	}
	if err := sys.WriteFileAtomic(filepath.Join(Dir(s.Name), specFile), b, 0o640); err != nil {
		return err
	}
	if s.Storage.Base != "" {
		if _, err := os.Stat(s.Storage.Base); err == nil {
			return sys.WriteFileAtomic(filepath.Join(s.Storage.Base, specFile), b, 0o640)
		}
	}
	return nil
}

// Names lists the defined containers.
func Names() []string {
	entries, _ := os.ReadDir(LXCPath)
	out := []string{}
	for _, e := range entries {
		if e.IsDir() && ValidName(e.Name()) == nil && Exists(e.Name()) {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out
}

// insideStrata validates a path below /srv/strata (no traversal, no
// characters the lxc config format cannot hold).
func insideStrata(p string) (string, error) {
	c := filepath.Clean(p)
	if !strings.HasPrefix(c, MountRoot+"/") || strings.ContainsAny(c, "\n\r\x00") || strings.Contains(c, "/.strata-lxc") {
		return "", fmt.Errorf("%s must be inside a volume (%s/...)", p, MountRoot)
	}
	return c, nil
}

// lxc runs an lxc-* tool on the StrataOS lxc path.
func lxc(tool string, name string, args ...string) (string, error) {
	return sys.Run(tool, append([]string{"-P", LXCPath, "-n", name}, args...)...)
}
