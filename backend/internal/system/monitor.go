// Package system exposes host information, monitoring, services, logs, updates and power control.
package system

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Sample struct {
	Time      int64   `json:"t"`
	CPU       float64 `json:"cpu"`
	MemUsed   int64   `json:"memUsed"`
	MemTotal  int64   `json:"memTotal"`
	NetRx     float64 `json:"netRx"` // bytes/s
	NetTx     float64 `json:"netTx"`
	DiskRead  float64 `json:"diskRead"` // bytes/s
	DiskWrite float64 `json:"diskWrite"`
}

// Monitor samples CPU, memory, network and disk throughput periodically.
type Monitor struct {
	mu        sync.RWMutex
	history   []Sample
	max       int
	prevCPU   [2]uint64
	prevNet   [2]uint64
	prevDsk   [2]uint64
	prevT     time.Time
	cores     []float64
	prevCores [][2]uint64
}

func NewMonitor() *Monitor {
	m := &Monitor{max: 180}
	go m.loop()
	return m
}

func (m *Monitor) loop() {
	m.sample()
	t := time.NewTicker(2 * time.Second)
	for range t.C {
		m.sample()
	}
}

func readCPU() (total, idle uint64, cores [][2]uint64) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if !strings.HasPrefix(l, "cpu") {
			break
		}
		fields := strings.Fields(l)
		var t, id uint64
		for i, v := range fields[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			if i < 8 { // exclude guest time (already in user)
				t += n
			}
			if i == 3 || i == 4 {
				id += n
			}
		}
		if fields[0] == "cpu" {
			total, idle = t, id
		} else {
			cores = append(cores, [2]uint64{t, id})
		}
	}
	return
}

func readNet() (rx, tx uint64) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(data), "\n")[2:] {
		name, rest, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if !physicalIface(name) {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		t, _ := strconv.ParseUint(f[8], 10, 64)
		rx += r
		tx += t
	}
	return
}

func physicalIface(name string) bool {
	if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") {
		return false
	}
	_, err := os.Stat(filepath.Join("/sys/class/net", name, "device"))
	return err == nil || strings.HasPrefix(name, "bond") || strings.HasPrefix(name, "br")
}

func readDisk() (rd, wr uint64) {
	data, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) < 10 {
			continue
		}
		name := f[2]
		// Only whole physical disks to avoid double counting partitions/md/dm.
		if _, err := os.Stat(filepath.Join("/sys/block", name, "device")); err != nil {
			continue
		}
		r, _ := strconv.ParseUint(f[5], 10, 64)
		w, _ := strconv.ParseUint(f[9], 10, 64)
		rd += r * 512
		wr += w * 512
	}
	return
}

// MemInfo returns total, available and swap values in bytes.
func MemInfo() map[string]int64 {
	out := map[string]int64{}
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, _ := strconv.ParseInt(f[0], 10, 64)
		out[k] = n * 1024
	}
	return out
}

func (m *Monitor) sample() {
	now := time.Now()
	total, idle, cores := readCPU()
	rx, tx := readNet()
	rd, wr := readDisk()
	mem := MemInfo()
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Sample{Time: now.UnixMilli(), MemTotal: mem["MemTotal"], MemUsed: mem["MemTotal"] - mem["MemAvailable"]}
	if !m.prevT.IsZero() {
		dt := now.Sub(m.prevT).Seconds()
		if dTotal := float64(total - m.prevCPU[0]); dTotal > 0 {
			s.CPU = 100 * (1 - float64(idle-m.prevCPU[1])/dTotal)
		}
		s.NetRx = float64(rx-m.prevNet[0]) / dt
		s.NetTx = float64(tx-m.prevNet[1]) / dt
		s.DiskRead = float64(rd-m.prevDsk[0]) / dt
		s.DiskWrite = float64(wr-m.prevDsk[1]) / dt
		if len(cores) == len(m.prevCores) {
			m.cores = make([]float64, len(cores))
			for i, c := range cores {
				if d := float64(c[0] - m.prevCores[i][0]); d > 0 {
					m.cores[i] = 100 * (1 - float64(c[1]-m.prevCores[i][1])/d)
				}
			}
		}
		m.history = append(m.history, s)
		if len(m.history) > m.max {
			m.history = m.history[len(m.history)-m.max:]
		}
	}
	m.prevCPU = [2]uint64{total, idle}
	m.prevNet = [2]uint64{rx, tx}
	m.prevDsk = [2]uint64{rd, wr}
	m.prevCores = cores
	m.prevT = now
}

func (m *Monitor) History() ([]Sample, []float64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h := make([]Sample, len(m.history))
	copy(h, m.history)
	c := make([]float64, len(m.cores))
	copy(c, m.cores)
	return h, c
}
