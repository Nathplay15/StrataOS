package lxc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/sys"
)

// NATBridge is the bridge of lxc-net (private network behind the NAS, DHCP
// and DNS by dnsmasq, masqueraded to the LAN).
var NATBridge = "lxcbr0"

// opMu serialises the operations that change a container (one at a time per name).
var opMu sync.Map

func lock(name string) func() {
	m, _ := opMu.LoadOrStore(name, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	if !mu.TryLock() {
		return nil
	}
	return mu.Unlock
}

func busy(name string) error { return fmt.Errorf("another operation is running on %s", name) }

// Container is a container and its live state.
type Container struct {
	Spec
	State     string   `json:"state"` // running | stopped | starting | stopping | frozen | unknown
	PID       int      `json:"pid,omitempty"`
	IPs       []string `json:"ips"`
	Snapshots int      `json:"snapshots"`
	// MemoryBytes and CPUUsec come from the container's cgroup (running only).
	MemoryBytes int64 `json:"memoryBytes,omitempty"`
	CPUUsec     int64 `json:"cpuUsec,omitempty"`
	// Missing: the volume holding the root file system is not there.
	Missing bool `json:"missing,omitempty"`
}

// parseInfo reads the output of lxc-info (State, PID and IP lines).
func parseInfo(out string) (state string, pid int, ips []string) {
	state = "unknown"
	ips = []string{}
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "State":
			state = strings.ToLower(v)
		case "PID":
			pid, _ = strconv.Atoi(v)
		case "IP":
			if v != "" && !slices.Contains(ips, v) {
				ips = append(ips, v)
			}
		}
	}
	return state, pid, ips
}

func info(name string) (string, int, []string) {
	out, err := lxc("lxc-info", name, "-s", "-p", "-i")
	if err != nil {
		return "unknown", 0, []string{}
	}
	return parseInfo(out)
}

// State returns the state of a container (running, stopped…).
func State(name string) string {
	s, _, _ := info(name)
	return s
}

// Get returns a container with its state.
func Get(name string) (*Container, error) {
	s, err := Load(name)
	if err != nil {
		return nil, err
	}
	c := &Container{Spec: *s, IPs: []string{}}
	c.State, c.PID, c.IPs = info(name)
	if _, err := os.Stat(s.Storage.Base); err != nil {
		c.Missing = true
	}
	c.Snapshots = len(snapshotMetas(s))
	if c.State == "running" && c.PID > 0 {
		if cg := cgroupOf(c.PID, name); cg != "" {
			c.MemoryBytes = readInt(filepath.Join(cg, "memory.current"))
			c.CPUUsec = statField(filepath.Join(cg, "cpu.stat"), "usage_usec")
		}
	}
	return c, nil
}

// List returns every container.
func List() []Container {
	out := []Container{}
	for _, n := range Names() {
		if c, err := Get(n); err == nil {
			out = append(out, *c)
		}
	}
	return out
}

// CreateReq is a new container.
type CreateReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Image       Image  `json:"image"`
	Privileged  bool   `json:"privileged"`
	// UIDs and GIDs keep their number inside an unprivileged container.
	UIDs     []int    `json:"uids"`
	GIDs     []int    `json:"gids"`
	Location string   `json:"location"`
	Backend  string   `json:"backend"`
	SizeGB   int      `json:"sizeGb"`
	CPUs     float64  `json:"cpus"`
	MemoryMB int      `json:"memoryMb"`
	SwapMB   int      `json:"swapMb"`
	Network  Network  `json:"network"`
	Mounts   []Mount  `json:"mounts"`
	Features Features `json:"features"`
	// Autostart: started with the NAS.
	Autostart      bool `json:"autostart"`
	AutostartOrder int  `json:"autostartOrder"`
	AutostartDelay int  `json:"autostartDelay"`
	Start          bool `json:"start"`
	// RootPassword is set with chpasswd once the container runs; never stored.
	RootPassword string `json:"rootPassword,omitempty"`
}

// CheckLocation validates a storage location and its backend.
func CheckLocation(location, backend string) (string, error) {
	loc, err := insideStrata(location)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(loc); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s not found", loc)
	}
	fs := fsType(loc)
	switch backend {
	case "dir", "image":
	case "btrfs":
		if fs != "btrfs" {
			return "", fmt.Errorf("%s is not on a Btrfs volume", loc)
		}
	case "zfs":
		if fs != "zfs" {
			return "", fmt.Errorf("%s is not a ZFS dataset", loc)
		}
	default:
		return "", fmt.Errorf("unknown storage backend %q", backend)
	}
	return loc, nil
}

// fsType returns the file system type of the mount holding p.
func fsType(p string) string {
	data, _ := os.ReadFile("/proc/self/mountinfo")
	best, typ := "", ""
	for _, l := range strings.Split(string(data), "\n") {
		left, right, ok := strings.Cut(l, " - ")
		if !ok {
			continue
		}
		f, r := strings.Fields(left), strings.Fields(right)
		if len(f) < 5 || len(r) < 1 {
			continue
		}
		mp := unescapeMount(f[4])
		if (p == mp || strings.HasPrefix(p, strings.TrimSuffix(mp, "/")+"/")) && len(mp) >= len(best) {
			best, typ = mp, r[0]
		}
	}
	return typ
}

// keyctlProfile writes lxc's default seccomp profile without its keyctl rule.
func keyctlProfile() string {
	b, err := os.ReadFile("/usr/share/lxc/config/common.seccomp")
	if err != nil {
		return ""
	}
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) > 0 && f[0] == "keyctl" {
			continue
		}
		keep = append(keep, l)
	}
	p := filepath.Join(CacheDir, "keyctl.seccomp")
	text := strings.Join(keep, "\n")
	if old, err := os.ReadFile(p); err == nil && string(old) == text {
		return p
	}
	if os.MkdirAll(CacheDir, 0o755) != nil || sys.WriteFileAtomic(p, []byte(text), 0o644) != nil {
		return ""
	}
	return p
}

func writeConfig(s *Spec) error {
	o := ConfigOpts{NATBridge: NATBridge}
	if s.Features.Keyctl {
		o.KeyctlSeccomp = keyctlProfile()
	}
	return sys.WriteFileAtomic(filepath.Join(Dir(s.Name), "config"), []byte(GenerateConfig(s, o)), 0o640)
}

// ensureTraverse lets the container's shifted root reach its root file
// system: search permission on the folders above it.
func ensureTraverse(s *Spec) {
	if s.Privileged {
		return
	}
	uid := strconv.Itoa(s.IDMap.Base)
	dirs := []string{Dir(s.Name)}
	// lxc also needs the lxc path itself to be reachable (search only:
	// nothing in it can be listed or read that other accounts cannot).
	for d := LXCPath; d != "/" && d != "."; d = filepath.Dir(d) {
		dirs = append(dirs, d)
	}
	for d := filepath.Dir(s.Storage.Base); strings.HasPrefix(d, MountRoot); d = filepath.Dir(d) {
		dirs = append(dirs, d)
	}
	for _, d := range dirs {
		fi, err := os.Stat(d)
		if err != nil || fi.Mode().Perm()&0o001 != 0 {
			continue
		}
		out, _ := sys.Run("getfacl", "-cp", d)
		if aclSearchable(out, uid) {
			continue
		}
		_, _ = sys.Run("setfacl", "-m", "u:"+uid+":x", d)
		// StrataOS sets 0700 on its data folder at each start, which empties
		// the ACL mask: give the mask its search bit back (group entries keep
		// their own bits, so nothing else changes).
		out, _ = sys.Run("getfacl", "-cp", d)
		if m := aclMask(out); m != "" && !aclSearchable(out, uid) {
			_, _ = sys.Run("setfacl", "-m", "m::"+m[:2]+"x", d)
		}
	}
}

// aclSearchable reports whether getfacl output gives uid an effective x.
func aclSearchable(out, uid string) bool {
	for _, l := range strings.Split(out, "\n") {
		entry, eff, _ := strings.Cut(l, "#effective:")
		if strings.TrimSpace(entry) == "user:"+uid+":--x" {
			return eff == "" || strings.Contains(eff, "x")
		}
	}
	return false
}

// aclMask returns the mask entry of getfacl output (e.g. "r-x").
func aclMask(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if m, ok := strings.CutPrefix(strings.TrimSpace(l), "mask::"); ok && len(m) == 3 {
			return m
		}
	}
	return ""
}

// Create makes a container from an image (a job: it downloads the image).
func Create(ctx context.Context, r CreateReq, logf func(string, ...any)) (err error) {
	if !Installed() {
		return fmt.Errorf("containers (LXC) are not installed")
	}
	unlock := lock(r.Name)
	if unlock == nil {
		return busy(r.Name)
	}
	defer func() { unlock() }()
	if err := ValidName(r.Name); err != nil {
		return err
	}
	if _, err := os.Stat(Dir(r.Name)); err == nil {
		return fmt.Errorf("a container named %s already exists", r.Name)
	}
	loc, err := CheckLocation(r.Location, r.Backend)
	if err != nil {
		return err
	}
	if r.SizeGB < 0 || r.SizeGB > 64*1024 || (r.Backend != "dir" && r.SizeGB < 1) {
		return fmt.Errorf("invalid disk size")
	}
	img, err := findImage(ctx, r.Image)
	if err != nil {
		return err
	}
	s := &Spec{Version: 1, Name: r.Name, Description: r.Description, Created: time.Now().UTC().Truncate(time.Second), Image: img,
		Privileged: r.Privileged, CPUs: r.CPUs, MemoryMB: r.MemoryMB, SwapMB: r.SwapMB, Network: r.Network, Mounts: r.Mounts,
		Features: r.Features, Autostart: r.Autostart, AutostartOrder: r.AutostartOrder, AutostartDelay: r.AutostartDelay,
		Storage: StorageSpec{Backend: r.Backend, Location: loc, Base: baseFor(loc, r.Name), SizeGB: r.SizeGB}}
	if !s.Privileged {
		// Checked before EnsureIDMap writes anything.
		probe := IDMap{Base: 1 << 20, Count: DefaultIDCount, UIDs: r.UIDs, GIDs: r.GIDs}
		if err := probe.validate(); err != nil {
			return err
		}
	}
	s.IDMap = IDMap{Base: 1 << 20, Count: DefaultIDCount}
	if err := s.Validate(); err != nil {
		return err
	}
	if !s.Privileged {
		if s.IDMap, err = EnsureIDMap(r.UIDs, r.GIDs); err != nil {
			return err
		}
	}
	if err := EnsureUnit(); err != nil {
		return err
	}
	be, err := BackendFor(s.Storage.Backend)
	if err != nil {
		return err
	}
	if err := mkBase(s.Storage.Base); err != nil {
		return err
	}
	created := false
	defer func() {
		if err != nil {
			logf("Removing what was created")
			if created {
				_ = be.Destroy(context.Background(), &s.Storage)
			} else {
				_ = removeBase(&s.Storage)
			}
			_ = os.RemoveAll(Dir(s.Name))
			removeDropIn(s.Name)
		}
	}()
	logf("Preparing the root file system (%s) in %s", s.Storage.Backend, s.Storage.Base)
	if err = be.Create(ctx, &s.Storage); err != nil {
		return err
	}
	created = true
	ensureTraverse(s)

	// The first configuration only gives the id mapping: the download
	// template then unpacks the image with the shifted owners.
	tmp, err := os.CreateTemp("", "strata-lxc-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	var first strings.Builder
	if !s.Privileged {
		for _, l := range s.IDMap.Lines() {
			fmt.Fprintf(&first, "lxc.idmap = %s\n", l)
		}
	}
	first.WriteString("lxc.net.0.type = empty\n")
	_, _ = tmp.WriteString(first.String())
	tmp.Close()

	logf("Downloading %s %s (%s) from %s, signature checked by the download template", img.Distro, img.Release, img.Arch, ImageServer)
	args := []string{"-P", LXCPath, "-n", s.Name, "-t", "download", "-B", "dir", "--dir", s.Storage.Rootfs(), "-f", tmp.Name(), "--",
		"--dist", img.Distro, "--release", img.Release, "--arch", img.Arch, "--variant", img.Variant, "--server", ImageServer}
	if _, err = sys.RunOpts(ctx, sys.Opts{Timeout: 45 * time.Minute, Output: &lineWriter{logf: logf}}, "lxc-create", args...); err != nil {
		return err
	}
	conf, _ := os.ReadFile(filepath.Join(Dir(s.Name), "config"))
	s.Includes, s.Arch = parseImageConfig(string(conf))
	if s.Arch == "" {
		s.Arch = img.Arch
	}
	if err = s.Validate(); err != nil {
		return err
	}
	if err = save(s); err != nil {
		return err
	}
	if err = writeConfig(s); err != nil {
		return err
	}
	if err = writeDropIn(s); err != nil {
		return err
	}
	if err = applyGuestNetwork(s); err != nil {
		logf("Network settings inside the container: %v", err)
		err = nil
	}
	logf("Container %s created", s.Name)
	if !r.Start {
		return nil
	}
	unlock()
	unlock = func() {}
	if err := Start(ctx, s.Name); err != nil {
		logf("The container did not start: %v", err)
		return nil
	}
	logf("Container %s started", s.Name)
	if r.RootPassword != "" {
		if _, stderr, code, err := Exec(ctx, s.Name, []string{"chpasswd"}, "root:"+r.RootPassword+"\n", time.Minute); err != nil || code != 0 {
			logf("The root password could not be set: %v %s", err, strings.TrimSpace(stderr))
		} else {
			logf("Root password set")
		}
	}
	return nil
}

type lineWriter struct {
	logf func(string, ...any)
	buf  []byte
}

func (w *lineWriter) Write(b []byte) (int, error) {
	w.buf = append(w.buf, b...)
	for {
		i := bytes.IndexAny(w.buf, "\r\n")
		if i < 0 {
			break
		}
		if l := strings.TrimSpace(string(w.buf[:i])); l != "" {
			w.logf("%s", l)
		}
		w.buf = w.buf[i+1:]
	}
	return len(b), nil
}

// ensureNAT starts lxc-net when its bridge is missing.
func ensureNAT() error {
	if _, err := os.Stat("/sys/class/net/" + NATBridge); err == nil {
		return nil
	}
	_, _ = sys.Run("systemctl", "enable", "--now", "lxc-net.service")
	if _, err := os.Stat("/sys/class/net/" + NATBridge); err != nil {
		return fmt.Errorf("the NAT network (%s) is not available: lxc-net did not start", NATBridge)
	}
	return nil
}

// prepare gets everything a start needs: storage mounted, config and unit up to date.
func prepare(ctx context.Context, s *Spec) error {
	if _, err := os.Stat(s.Storage.Base); err != nil {
		return fmt.Errorf("the volume holding %s is not available (%s)", s.Name, s.Storage.Location)
	}
	be, err := BackendFor(s.Storage.Backend)
	if err != nil {
		return err
	}
	if err := be.Prepare(ctx, &s.Storage); err != nil {
		return err
	}
	switch s.Network.Mode {
	case "nat":
		if err := ensureNAT(); err != nil {
			return err
		}
	case "bridge":
		if _, err := os.Stat("/sys/class/net/" + s.Network.Bridge + "/bridge"); err != nil {
			return fmt.Errorf("%s is not a bridge (create one in Network)", s.Network.Bridge)
		}
	}
	for _, m := range s.Mounts {
		if fi, err := os.Stat(m.Source); err != nil || !fi.IsDir() {
			return fmt.Errorf("shared folder %s (%s) is not available", m.Share, m.Source)
		}
	}
	if err := EnsureUnit(); err != nil {
		return err
	}
	ensureTraverse(s)
	if err := writeConfig(s); err != nil {
		return err
	}
	return writeDropIn(s)
}

func unitActive(name string) string {
	out, _ := sys.RunOpts(context.Background(), sys.Opts{AllowCodes: []int{-2}}, "systemctl", "is-active", unitName(name))
	return strings.TrimSpace(out)
}

// Start starts a container and waits until it runs.
func Start(ctx context.Context, name string) error {
	unlock := lock(name)
	if unlock == nil {
		return busy(name)
	}
	defer func() { unlock() }()
	s, err := Load(name)
	if err != nil {
		return err
	}
	if st := State(name); st == "running" {
		return nil
	}
	if err := prepare(ctx, s); err != nil {
		return err
	}
	_, _ = sys.Run("systemctl", "reset-failed", unitName(name))
	if _, err := sys.Run("systemctl", "start", unitName(name)); err != nil {
		return fmt.Errorf("%v\n%s", err, unitLog(name))
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if State(name) == "running" {
			return nil
		}
		if a := unitActive(name); a == "failed" || a == "inactive" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if State(name) == "running" {
		return nil
	}
	return fmt.Errorf("%s did not start:\n%s", name, unitLog(name))
}

// Stop asks the container to shut down (init gets the signal; lxc kills it
// after two minutes). It returns at once.
func Stop(name string) error {
	if _, err := Load(name); err != nil {
		return err
	}
	if unitActive(name) == "active" || unitActive(name) == "activating" {
		_, err := sys.Run("systemctl", "stop", "--no-block", unitName(name))
		return err
	}
	// Started outside its unit (by hand): stop it directly.
	if State(name) == "running" {
		cmd := exec.Command("lxc-stop", "-P", LXCPath, "-n", name, "-t", "120")
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
	}
	return nil
}

// Kill stops a container at once (like pulling the plug).
func Kill(name string) error {
	if _, err := Load(name); err != nil {
		return err
	}
	if State(name) == "running" || State(name) == "stopping" || State(name) == "starting" {
		if _, err := lxc("lxc-stop", name, "-k"); err != nil && State(name) != "stopped" {
			return err
		}
	}
	_, _ = sys.Run("systemctl", "stop", "--no-block", unitName(name))
	return nil
}

// Restart reboots a container: a clean shutdown, then a start, in its unit.
func Restart(ctx context.Context, name string) error {
	s, err := Load(name)
	if err != nil {
		return err
	}
	if State(name) != "running" {
		return Start(ctx, name)
	}
	if err := prepare(ctx, s); err != nil {
		return err
	}
	_, err = sys.Run("systemctl", "restart", "--no-block", unitName(name))
	return err
}

// WaitState waits until a container reaches a state.
func WaitState(ctx context.Context, name, state string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if State(name) == state {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return State(name) == state
}

// Settings are the changeable parts of a container.
type Settings struct {
	Description    string   `json:"description"`
	CPUs           float64  `json:"cpus"`
	MemoryMB       int      `json:"memoryMb"`
	SwapMB         int      `json:"swapMb"`
	SizeGB         int      `json:"sizeGb"`
	Network        Network  `json:"network"`
	Mounts         []Mount  `json:"mounts"`
	Features       Features `json:"features"`
	Autostart      bool     `json:"autostart"`
	AutostartOrder int      `json:"autostartOrder"`
	AutostartDelay int      `json:"autostartDelay"`
	// UIDs and GIDs (unprivileged): host ids kept inside; applied at the next start.
	UIDs []int `json:"uids"`
	GIDs []int `json:"gids"`
}

// Update changes a container. Limits apply at once to a running container;
// the rest (network, mounts, features, id mapping) at its next start, which
// the result reports.
func Update(ctx context.Context, name string, set Settings) (restart bool, err error) {
	unlock := lock(name)
	if unlock == nil {
		return false, busy(name)
	}
	defer func() { unlock() }()
	s, err := Load(name)
	if err != nil {
		return false, err
	}
	old := *s
	n := *s
	n.Description, n.CPUs, n.MemoryMB, n.SwapMB = set.Description, set.CPUs, set.MemoryMB, set.SwapMB
	n.Network, n.Mounts, n.Features = set.Network, set.Mounts, set.Features
	if n.Network.MAC == "" {
		n.Network.MAC = s.Network.MAC
	}
	n.Autostart, n.AutostartOrder, n.AutostartDelay = set.Autostart, set.AutostartOrder, set.AutostartDelay
	if !n.Privileged {
		n.IDMap.UIDs, n.IDMap.GIDs = set.UIDs, set.GIDs
	}
	if err := n.Validate(); err != nil {
		return false, err
	}
	if !n.Privileged && (!slices.Equal(n.IDMap.UIDs, old.IDMap.UIDs) || !slices.Equal(n.IDMap.GIDs, old.IDMap.GIDs)) {
		if err := recordIDs(n.IDMap); err != nil {
			return false, err
		}
	}
	running := State(name) == "running"
	// 0: size unchanged.
	if set.SizeGB != 0 && set.SizeGB != s.Storage.SizeGB && n.Storage.Backend != "dir" {
		be, err := BackendFor(n.Storage.Backend)
		if err != nil {
			return false, err
		}
		if err := be.Resize(ctx, &n.Storage, set.SizeGB); err != nil {
			return false, err
		}
	}
	if err := save(&n); err != nil {
		return false, err
	}
	if err := writeConfig(&n); err != nil {
		return false, err
	}
	if err := writeDropIn(&n); err != nil {
		return false, err
	}
	netChanged := !networkEqual(n.Network, old.Network)
	if netChanged {
		if err := applyGuestNetwork(&n); err != nil {
			return false, fmt.Errorf("network settings inside the container: %w", err)
		}
	}
	if running {
		applyLimits(name, &n)
		restart = netChanged || !slices.Equal(n.Mounts, old.Mounts) || n.Features != old.Features ||
			!slices.Equal(n.IDMap.UIDs, old.IDMap.UIDs) || !slices.Equal(n.IDMap.GIDs, old.IDMap.GIDs)
	}
	return restart, nil
}

func networkEqual(a, b Network) bool {
	return a.Mode == b.Mode && a.Bridge == b.Bridge && a.MAC == b.MAC && a.Address == b.Address && a.Gateway == b.Gateway && slices.Equal(a.DNS, b.DNS)
}

// applyLimits writes the CPU and memory limits into a running container's cgroup.
func applyLimits(name string, s *Spec) {
	_, pid, _ := info(name)
	cg := cgroupOf(pid, name)
	if cg == "" {
		return
	}
	cpu := "max 100000"
	if s.CPUs > 0 {
		cpu = fmt.Sprintf("%d 100000", int(s.CPUs*100000+0.5))
	}
	mem, swap := "max", "max"
	if s.MemoryMB > 0 {
		mem, swap = strconv.Itoa(s.MemoryMB<<20), strconv.Itoa(s.SwapMB<<20)
	}
	_ = os.WriteFile(filepath.Join(cg, "cpu.max"), []byte(cpu), 0)
	_ = os.WriteFile(filepath.Join(cg, "memory.max"), []byte(mem), 0)
	_ = os.WriteFile(filepath.Join(cg, "memory.swap.max"), []byte(swap), 0)
}

// Delete removes a stopped container, its root file system and its snapshots.
func Delete(ctx context.Context, name string) error {
	unlock := lock(name)
	if unlock == nil {
		return busy(name)
	}
	defer func() { unlock() }()
	s, err := Load(name)
	if err != nil {
		return err
	}
	if st := State(name); st != "stopped" && st != "unknown" {
		return fmt.Errorf("stop %s first", name)
	}
	_, _ = sys.Run("systemctl", "stop", unitName(name))
	if _, err := os.Stat(s.Storage.Base); err == nil {
		be, err := BackendFor(s.Storage.Backend)
		if err != nil {
			return err
		}
		if err := be.Destroy(ctx, &s.Storage); err != nil {
			return err
		}
	}
	removeDropIn(name)
	return os.RemoveAll(Dir(name))
}

// Clone copies a stopped container into a new one (new MAC address and host
// name, not started with the NAS).
func Clone(ctx context.Context, src, dst string, logf func(string, ...any)) (err error) {
	if err := ValidName(dst); err != nil {
		return err
	}
	unlock := lock(src)
	if unlock == nil {
		return busy(src)
	}
	defer func() { unlock() }()
	s, err := Load(src)
	if err != nil {
		return err
	}
	if State(src) != "stopped" {
		return fmt.Errorf("stop %s first", src)
	}
	if _, err := os.Stat(Dir(dst)); err == nil {
		return fmt.Errorf("a container named %s already exists", dst)
	}
	be, err := BackendFor(s.Storage.Backend)
	if err != nil {
		return err
	}
	if err := be.Prepare(ctx, &s.Storage); err != nil {
		return err
	}
	n := *s
	n.Name, n.Created, n.Autostart = dst, time.Now().UTC().Truncate(time.Second), false
	n.Mounts = slices.Clone(s.Mounts)
	n.Storage.Base, n.Storage.Dataset = baseFor(s.Storage.Location, dst), ""
	if n.Network.Mode != "none" {
		n.Network.MAC = NewMAC()
		if n.Network.Address != "" {
			// Two containers cannot share an address: the copy uses DHCP.
			n.Network.Address, n.Network.Gateway, n.Network.DNS = "", "", nil
		}
	}
	if err := mkBase(n.Storage.Base); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = be.Destroy(context.Background(), &n.Storage)
			_ = os.RemoveAll(Dir(dst))
			removeDropIn(dst)
		}
	}()
	logf("Copying the root file system of %s", src)
	if err = be.Clone(ctx, &s.Storage, &n.Storage); err != nil {
		return err
	}
	if err = setGuestHostname(&n, src); err != nil {
		logf("Host name inside the copy: %v", err)
	}
	if err = applyGuestNetwork(&n); err != nil {
		logf("Network settings inside the copy: %v", err)
	}
	err = nil
	if err = save(&n); err != nil {
		return err
	}
	if err = writeConfig(&n); err != nil {
		return err
	}
	return writeDropIn(&n)
}

// Rename gives a stopped container another name (and host name).
func Rename(ctx context.Context, name, newName string) error {
	if err := ValidName(newName); err != nil {
		return err
	}
	unlock := lock(name)
	if unlock == nil {
		return busy(name)
	}
	defer func() { unlock() }()
	s, err := Load(name)
	if err != nil {
		return err
	}
	if State(name) != "stopped" {
		return fmt.Errorf("stop %s first", name)
	}
	if _, err := os.Stat(Dir(newName)); err == nil {
		return fmt.Errorf("a container named %s already exists", newName)
	}
	be, err := BackendFor(s.Storage.Backend)
	if err != nil {
		return err
	}
	if err := be.Rename(ctx, &s.Storage, newName); err != nil {
		return err
	}
	removeDropIn(name)
	if err := os.Rename(Dir(name), Dir(newName)); err != nil {
		return err
	}
	s.Name = newName
	_ = setGuestHostname(s, name)
	if err := save(s); err != nil {
		return err
	}
	if err := writeConfig(s); err != nil {
		return err
	}
	return writeDropIn(s)
}

// Exec runs a command in a running container (as its root, clean
// environment) and returns its output and exit code.
func Exec(ctx context.Context, name string, argv []string, stdin string, timeout time.Duration) (stdout, stderr string, code int, err error) {
	if err := ValidName(name); err != nil {
		return "", "", -1, err
	}
	if len(argv) == 0 || len(argv) > 256 {
		return "", "", -1, fmt.Errorf("no command")
	}
	if State(name) != "running" {
		return "", "", -1, fmt.Errorf("%s is not running", name)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := append(attachArgs(name, "dumb"), "--")
	cmd := exec.CommandContext(ctx, "lxc-attach", append(args, argv...)...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limitedBuffer{b: &o, max: 4 << 20}, &limitedBuffer{b: &e, max: 1 << 20}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	err = cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return o.String(), e.String(), ee.ExitCode(), nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return o.String(), e.String(), -1, fmt.Errorf("timed out after %s", timeout)
	}
	if err != nil {
		return o.String(), e.String(), -1, err
	}
	return o.String(), e.String(), 0, nil
}

// attachArgs are the lxc-attach options of a shell or command: a clean
// environment, so nothing of StrataOS's leaks into the container.
func attachArgs(name, term string) []string {
	return []string{"-P", LXCPath, "-n", name, "--clear-env",
		"-v", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "-v", "HOME=/root", "-v", "TERM=" + term, "-v", "LANG=C.UTF-8"}
}

// ShellCmd is the interactive shell of a container (root's login shell).
func ShellCmd(ctx context.Context, name string) *exec.Cmd {
	return exec.CommandContext(ctx, "lxc-attach", attachArgs(name, "xterm-256color")...)
}

// ConsoleCmd attaches to the container's console (/dev/console, login prompt).
// Ctrl+a q detaches.
func ConsoleCmd(ctx context.Context, name string) *exec.Cmd {
	return exec.CommandContext(ctx, "lxc-console", "-P", LXCPath, "-n", name, "-t", "0", "-e", "a")
}

type limitedBuffer struct {
	b   *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

var snapRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]{0,62}$`)
