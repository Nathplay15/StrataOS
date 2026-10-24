package lxc

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Resource use, read from the container's cgroup v2 (lxc.payload.<name>)
// and, for the network, from /proc/<init>/net/dev (the container's own
// network namespace).

// cgroupOf returns the cgroup folder of a running container.
func cgroupOf(pid int, name string) string {
	if pid <= 0 {
		return ""
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return ""
	}
	return payloadCgroup(string(b), name)
}

// payloadCgroup finds the lxc.payload.<name> folder in a /proc/<pid>/cgroup.
func payloadCgroup(content, name string) string {
	for _, l := range strings.Split(content, "\n") {
		p, ok := strings.CutPrefix(strings.TrimSpace(l), "0::")
		if !ok {
			continue
		}
		parts := strings.Split(p, "/")
		for i, c := range parts {
			if c == "lxc.payload."+name {
				return filepath.Join("/sys/fs/cgroup", filepath.Join(parts[:i+1]...))
			}
		}
	}
	return ""
}

func readInt(p string) int64 {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// readLimit reads a cgroup limit ("max" is 0: none).
func readLimit(p string) int64 {
	b, err := os.ReadFile(p)
	if err != nil || strings.TrimSpace(string(b)) == "max" {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// statField reads "key value" lines (cpu.stat, memory.stat).
func statField(p, key string) int64 {
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), " ")
		if ok && k == key {
			n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

// parseIOStat sums the bytes read and written of io.stat.
func parseIOStat(content string) (rd, wr int64) {
	for _, l := range strings.Split(content, "\n") {
		for _, f := range strings.Fields(l) {
			k, v, ok := strings.Cut(f, "=")
			if !ok {
				continue
			}
			n, _ := strconv.ParseInt(v, 10, 64)
			switch k {
			case "rbytes":
				rd += n
			case "wbytes":
				wr += n
			}
		}
	}
	return rd, wr
}

// parseNetDev sums the bytes received and sent on every interface but lo.
func parseNetDev(content string) (rx, tx int64) {
	for _, l := range strings.Split(content, "\n") {
		iface, rest, ok := strings.Cut(l, ":")
		if !ok || strings.TrimSpace(iface) == "lo" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 16 {
			continue
		}
		r, _ := strconv.ParseInt(f[0], 10, 64)
		t, _ := strconv.ParseInt(f[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx
}

// Stats is one sample of a container's resource use (counters: the caller
// computes rates from two samples).
type Stats struct {
	Time       time.Time `json:"time"`
	State      string    `json:"state"`
	CPUUsec    int64     `json:"cpuUsec"`
	CPULimit   float64   `json:"cpuLimit"` // cores, 0 = none
	Cores      int       `json:"cores"`    // of the NAS
	Memory     int64     `json:"memory"`
	MemoryMax  int64     `json:"memoryMax"`
	Swap       int64     `json:"swap"`
	SwapMax    int64     `json:"swapMax"`
	Pids       int64     `json:"pids"`
	NetRx      int64     `json:"netRx"`
	NetTx      int64     `json:"netTx"`
	DiskRead   int64     `json:"diskRead"`
	DiskWrite  int64     `json:"diskWrite"`
	DiskUsed   int64     `json:"diskUsed"`
	DiskLimit  int64     `json:"diskLimit"`
	IPs        []string  `json:"ips"`
	CgroupPath string    `json:"-"`
}

// GetStats samples a container. withDisk also measures the root file system
// (slow for a plain folder: du).
func GetStats(name string, withDisk bool) (*Stats, error) {
	s, err := Load(name)
	if err != nil {
		return nil, err
	}
	st := &Stats{Time: time.Now(), CPULimit: s.CPUs, Cores: numCPU(), IPs: []string{}}
	var pid int
	st.State, pid, st.IPs = info(name)
	if cg := cgroupOf(pid, name); cg != "" {
		st.CgroupPath = cg
		st.CPUUsec = statField(filepath.Join(cg, "cpu.stat"), "usage_usec")
		st.Memory = readInt(filepath.Join(cg, "memory.current"))
		st.MemoryMax = readLimit(filepath.Join(cg, "memory.max"))
		st.Swap = readInt(filepath.Join(cg, "memory.swap.current"))
		st.SwapMax = readLimit(filepath.Join(cg, "memory.swap.max"))
		st.Pids = readInt(filepath.Join(cg, "pids.current"))
		if b, err := os.ReadFile(filepath.Join(cg, "io.stat")); err == nil {
			st.DiskRead, st.DiskWrite = parseIOStat(string(b))
		}
		if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/dev", pid)); err == nil {
			st.NetRx, st.NetTx = parseNetDev(string(b))
		}
	}
	if withDisk {
		if be, err := BackendFor(s.Storage.Backend); err == nil {
			st.DiskUsed, st.DiskLimit = be.Usage(&s.Storage)
		}
	}
	return st, nil
}

func numCPU() int { return runtime.NumCPU() }
