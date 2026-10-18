package api

// Prometheus metrics at /metrics (Bearer token from the settings), and a
// ready-made Grafana dashboard.

import (
	"bufio"
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/crypt"
	"strataos/internal/energy"
	"strataos/internal/ha"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/ups"
)

// smartCache keeps the last SMART reading of each awake disk (the health
// watcher fills it), so that scrapes never wake up a sleeping disk.
var smartCache sync.Map // device → smartEntry

type smartEntry struct {
	info          storage.SmartInfo
	model, serial string
}

type promWriter struct {
	b    strings.Builder
	seen map[string]bool
}

func esc(v string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(v)
}

// m writes one sample; help/type lines are written once per metric.
func (w *promWriter) m(name, typ, help string, value float64, labels ...string) {
	if !w.seen[name] {
		w.seen[name] = true
		fmt.Fprintf(&w.b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	w.b.WriteString(name)
	if len(labels) > 0 {
		w.b.WriteString("{")
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				w.b.WriteString(",")
			}
			fmt.Fprintf(&w.b, `%s="%s"`, labels[i], esc(labels[i+1]))
		}
		w.b.WriteString("}")
	}
	fmt.Fprintf(&w.b, " %s\n", strconv.FormatFloat(value, 'g', -1, 64))
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

var diskStatRe = regexp.MustCompile(`^(sd[a-z]+|vd[a-z]+|nvme\d+n\d+|md\d+|xvd[a-z]+)$`)

func procMetrics(w *promWriter) {
	if f, err := os.Open("/proc/stat"); err == nil {
		sc := bufio.NewScanner(f)
		modes := []string{"user", "nice", "system", "idle", "iowait", "irq", "softirq", "steal"}
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) > 8 && fl[0] == "cpu" {
				for i, m := range modes {
					v, _ := strconv.ParseFloat(fl[i+1], 64)
					w.m("strata_cpu_seconds_total", "counter", "CPU time by mode (all cores).", v/100, "mode", m)
				}
			}
		}
		f.Close()
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		fl := strings.Fields(string(b))
		for i, n := range []string{"1", "5", "15"} {
			if len(fl) > i {
				v, _ := strconv.ParseFloat(fl[i], 64)
				w.m("strata_load", "gauge", "Load average.", v, "period", n)
			}
		}
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) < 2 {
				continue
			}
			v, _ := strconv.ParseFloat(fl[1], 64)
			switch fl[0] {
			case "MemTotal:":
				w.m("strata_memory_total_bytes", "gauge", "Total memory.", v*1024)
			case "MemAvailable:":
				w.m("strata_memory_available_bytes", "gauge", "Available memory.", v*1024)
			}
		}
		f.Close()
	}
	if f, err := os.Open("/proc/net/dev"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			name, rest, ok := strings.Cut(sc.Text(), ":")
			name = strings.TrimSpace(name)
			if !ok || name == "lo" || strings.HasPrefix(name, "veth") {
				continue
			}
			fl := strings.Fields(rest)
			if len(fl) >= 9 {
				rx, _ := strconv.ParseFloat(fl[0], 64)
				tx, _ := strconv.ParseFloat(fl[8], 64)
				w.m("strata_network_receive_bytes_total", "counter", "Bytes received.", rx, "interface", name)
				w.m("strata_network_transmit_bytes_total", "counter", "Bytes sent.", tx, "interface", name)
			}
		}
		f.Close()
	}
	if f, err := os.Open("/proc/diskstats"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) < 10 || !diskStatRe.MatchString(fl[2]) {
				continue
			}
			rd, _ := strconv.ParseFloat(fl[5], 64)
			wr, _ := strconv.ParseFloat(fl[9], 64)
			w.m("strata_disk_read_bytes_total", "counter", "Bytes read.", rd*512, "device", fl[2])
			w.m("strata_disk_written_bytes_total", "counter", "Bytes written.", wr*512, "device", fl[2])
		}
		f.Close()
	}
}

func (s *Server) metricsText() string {
	w := &promWriter{seen: map[string]bool{}}
	host, _ := hostname()
	w.m("strata_info", "gauge", "StrataOS version.", 1, "version", s.version, "hostname", host)
	procMetrics(w)

	raids, _ := storage.ListRaid()
	pools, _ := storage.ListZPools(false)
	for _, v := range s.volumeSummaries(raids, pools) {
		w.m("strata_volume_size_bytes", "gauge", "Volume size.", float64(v.Total), "volume", v.Name, "fstype", v.Kind)
		w.m("strata_volume_used_bytes", "gauge", "Volume used space.", float64(v.Used), "volume", v.Name, "fstype", v.Kind)
		w.m("strata_volume_healthy", "gauge", "1 when the volume is healthy and mounted.", b2f(v.Health == "healthy"), "volume", v.Name)
	}
	for _, r := range raids {
		w.m("strata_raid_healthy", "gauge", "1 when the RAID array is healthy (0: degraded, rebuilding, failed or inactive).", b2f(r.Health == "healthy"), "array", r.Name, "level", r.Level, "health", r.Health)
	}
	for _, p := range pools {
		w.m("strata_zpool_healthy", "gauge", "1 when the ZFS pool is ONLINE.", b2f(p.Health == "ONLINE"), "pool", p.Name)
	}
	smartCache.Range(func(k, v any) bool {
		e := v.(smartEntry)
		dev := k.(string)
		if e.info.Temperature > 0 {
			w.m("strata_disk_temperature_celsius", "gauge", "Disk temperature (last SMART reading of an awake disk).", float64(e.info.Temperature), "device", dev, "model", e.model, "serial", e.serial)
		}
		if e.info.Passed != nil {
			w.m("strata_disk_smart_healthy", "gauge", "1 when the disk passes its SMART health check.", b2f(*e.info.Passed && len(e.info.Warnings) == 0), "device", dev, "model", e.model, "serial", e.serial)
		}
		if e.info.PowerOnHours > 0 {
			w.m("strata_disk_power_on_hours", "gauge", "Disk power-on hours.", float64(e.info.PowerOnHours), "device", dev, "serial", e.serial)
		}
		return true
	})
	for _, d := range rotatingDisks() {
		w.m("strata_disk_standby", "gauge", "1 when the hard disk is spun down.", b2f(energy.DiskState(d.Path) == "standby"), "device", d.Path)
	}

	var st store.State
	s.store.Read(func(x *store.State) {
		st.Shares, st.Backups, st.HA, st.Guard, st.Ransom, st.Links = x.Shares, x.Backups, x.HA, x.Guard, x.Ransom, x.Links
	})
	for _, sh := range st.Shares {
		w.m("strata_share_used_bytes", "gauge", "Space used by the shared folder (measured when it has a quota).", float64(sh.Used), "share", sh.Name)
		if sh.QuotaGB > 0 {
			w.m("strata_share_quota_bytes", "gauge", "Quota of the shared folder.", sh.QuotaGB*1e9, "share", sh.Name)
		}
		if sh.Encrypted {
			w.m("strata_share_locked", "gauge", "1 when the encrypted share is locked.", b2f(!crypt.Mounted(sh.Path)), "share", sh.Name)
		}
	}
	for _, b := range st.Backups {
		var lastOK time.Time
		status := -1.0
		if len(b.History) > 0 {
			status = b2f(b.History[0].Status == "success")
		}
		for _, h := range b.History {
			if h.Status == "success" {
				lastOK = h.Started
				break
			}
		}
		w.m("strata_backup_last_status", "gauge", "1 if the last run succeeded, 0 if it failed, -1 if never run.", status, "task", b.Name, "kind", b.Kind)
		if !lastOK.IsZero() {
			w.m("strata_backup_last_success_timestamp_seconds", "gauge", "Time of the last successful run.", float64(lastOK.Unix()), "task", b.Name)
		}
	}
	running := 0
	for _, j := range s.jobs.List() {
		if j.Status == "running" {
			running++
		}
	}
	w.m("strata_jobs_running", "gauge", "Background tasks running.", float64(running))
	if st.HA.Role != "" {
		w.m("strata_ha_active", "gauge", "1 when this NAS holds the shared address.", b2f(ha.State() == "MASTER"), "role", st.HA.Role)
	}
	blocked := 0
	for _, b := range st.Guard.Blocks {
		if time.Now().Before(b.Until) {
			blocked++
		}
	}
	w.m("strata_guard_blocked_addresses", "gauge", "Addresses blocked now.", float64(blocked))
	w.m("strata_ransomware_blocked_accounts", "gauge", "Accounts blocked by the ransomware protection.", float64(len(st.Ransom.Blocked)))
	w.m("strata_share_links", "gauge", "Public share links.", float64(len(st.Links)))
	if c := s.upsConfig(); c.Mode != "off" && ups.Installed() {
		if v, err := ups.Status(c); err == nil {
			for k, name := range map[string]string{"battery.charge": "strata_ups_battery_charge_percent", "ups.load": "strata_ups_load_percent", "battery.runtime": "strata_ups_runtime_seconds"} {
				if f, err := strconv.ParseFloat(v[k], 64); err == nil {
					w.m(name, "gauge", "UPS "+k+".", f)
				}
			}
			w.m("strata_ups_on_battery", "gauge", "1 when running on battery.", b2f(strings.HasPrefix(v["ups.status"], "OB")))
		}
	}
	alerts := map[string]int{"error": 0, "warning": 0}
	for _, a := range s.alerts(raids, pools, nil) {
		alerts[a.Level]++
	}
	keys := make([]string, 0, len(alerts))
	for k := range alerts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w.m("strata_alerts", "gauge", "Active alerts by level.", float64(alerts[k]), "level", k)
	}
	return w.b.String()
}

func (s *Server) metricsRoutes() {
	s.mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		var tok string
		s.store.Read(func(st *store.State) { tok = st.Settings.MetricsToken })
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" {
			http.NotFound(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
			s.guardFail(clientIP(r), "Prometheus", "")
			w.Header().Set("WWW-Authenticate", `Bearer realm="StrataOS"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(s.metricsText()))
	})

	// Turns the endpoint on (new token) or off.
	s.admin("PUT /api/metrics", func(r *http.Request) (any, error) {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		tok := ""
		if req.Enabled {
			tok = randHex(24)
		}
		if err := s.store.Update(func(st *store.State) error { st.Settings.MetricsToken = tok; return nil }); err != nil {
			return nil, err
		}
		s.audit(r, "system", fmt.Sprintf("Prometheus metrics enabled=%v", req.Enabled))
		return map[string]string{"token": tok}, nil
	})
	s.admin("GET /api/metrics/grafana", func(r *http.Request) (any, error) { return grafanaDashboard(), nil })
}
