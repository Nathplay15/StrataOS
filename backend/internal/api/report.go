package api

import (
	"fmt"
	"net/http"
	"os"
	"strataos/internal/i18n"
	"strconv"
	"strings"
	"time"

	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

func gb(n int64) string {
	switch {
	case n >= 1e12:
		return fmt.Sprintf("%.2f TB", float64(n)/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	default:
		return fmt.Sprintf("%.0f MB", float64(n)/1e6)
	}
}

func uptimeDays() int {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f, _ := strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	return int(f / 86400)
}

// monthlyReport summarises the health of the NAS over the last 30 days.
func (s *Server) monthlyReport() (string, string) {
	host, _ := hostname()
	since := time.Now().AddDate(0, 0, -30)
	var b strings.Builder
	subject := "Monthly report of " + host
	raids, _ := storage.ListRaid()
	pools, _ := storage.ListZPools(false)
	vols := s.volumeSummaries(raids, pools)
	alerts := s.alerts(raids, pools, vols)

	b.WriteString("Storage\n")
	for _, v := range vols {
		pct := 0.0
		if v.Total > 0 {
			pct = float64(v.Used) / float64(v.Total) * 100
		}
		fmt.Fprintf(&b, "• %s: %s used of %s (%.0f%%)\n", v.Name, gb(v.Used), gb(v.Total), pct)
	}
	if len(vols) == 0 {
		b.WriteString("• no volume\n")
	}

	b.WriteString("\nHealth\n")
	if len(alerts) == 0 {
		b.WriteString("• everything is healthy: disks, RAID arrays, ZFS pools and volumes\n")
	}
	for _, a := range alerts {
		fmt.Fprintf(&b, "• %s\n", a.Message)
	}
	disks, _ := storage.ListDisks()
	fmt.Fprintf(&b, "• %d disk(s) monitored\n", len(disks))

	var backupsOK, backupsKO, cloudOK, cloudKO, avScans, avInfected int
	var lastFail, quotaLines []string
	s.store.Read(func(st *store.State) {
		for _, t := range st.Backups {
			for _, h := range t.History {
				if h.Started.Before(since) {
					continue
				}
				if h.Status == "success" {
					backupsOK++
				} else {
					backupsKO++
					lastFail = append(lastFail, t.Name)
				}
			}
		}
		for _, t := range st.Clouds {
			for _, h := range t.History {
				if h.Started.After(since) {
					if h.Status == "success" {
						cloudOK++
					} else {
						cloudKO++
					}
				}
			}
		}
		for _, sc := range st.Schedules {
			if sc.Type != "antivirus" {
				continue
			}
			for _, h := range sc.History {
				if h.Started.After(since) {
					avScans++
					if h.Status != "success" {
						avInfected++
					}
				}
			}
		}
		for _, sh := range st.Shares {
			if sh.QuotaGB > 0 {
				quotaLines = append(quotaLines, fmt.Sprintf("• %s: %s of %.0f GB", sh.Name, gb(sh.Used), sh.QuotaGB))
			}
		}
	})
	b.WriteString("\nBackups (30 days)\n")
	if backupsOK+backupsKO == 0 {
		b.WriteString("• no backup ran: consider scheduling one\n")
	} else {
		fmt.Fprintf(&b, "• %d succeeded, %d failed", backupsOK, backupsKO)
		if len(lastFail) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(uniq(lastFail), ", "))
		}
		b.WriteString("\n")
	}
	if cloudOK+cloudKO > 0 {
		fmt.Fprintf(&b, "• cloud sync: %d succeeded, %d failed\n", cloudOK, cloudKO)
	}
	if avScans > 0 {
		fmt.Fprintf(&b, "• antivirus: %d scan(s), %d with infected files\n", avScans, avInfected)
	}
	if len(quotaLines) > 0 {
		b.WriteString("\nShared folder quotas\n" + strings.Join(quotaLines, "\n") + "\n")
	}
	b.WriteString("\nSystem\n")
	if out, err := sys.Run("apt-get", "-s", "-o", "Debug::NoLocking=1", "upgrade"); err == nil {
		n := strings.Count(out, "\nInst ")
		if strings.HasPrefix(out, "Inst ") {
			n++
		}
		if n > 0 {
			fmt.Fprintf(&b, "• %d update(s) available\n", n)
		} else {
			b.WriteString("• the system is up to date\n")
		}
	}
	fmt.Fprintf(&b, "• StrataOS %s, running for %d day(s)\n", s.version, uptimeDays())
	return subject, b.String()
}

func uniq(l []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range l {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func (s *Server) reportRoutes() {
	s.admin("POST /api/settings/report", func(r *http.Request) (any, error) {
		subject, body := s.monthlyReport()
		var lang string
		s.store.Read(func(st *store.State) { lang = st.Settings.NotifyLang })
		shown := i18n.Lines(lang, body)
		if _, err := s.sendNotification("", subject, body); err != nil {
			return map[string]string{"report": shown, "warning": err.Error()}, nil
		}
		return map[string]string{"report": shown}, nil
	})
}
