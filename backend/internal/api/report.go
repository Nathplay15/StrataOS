package api

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"strataos/internal/i18n"
	"strconv"
	"strings"
	"time"

	"strataos/internal/health"
	"strataos/internal/storage"
	"strataos/internal/store"
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

// reportDue tells which report is due at this minute ("" = none): weekly on
// Mondays, monthly on the 1st, at 8 AM.
func (s *Server) reportDue(tick time.Time) string {
	if tick.Hour() != 8 || tick.Minute() != 0 {
		return ""
	}
	var p string
	s.store.Read(func(st *store.State) { p = st.Settings.ReportPeriod() })
	if (p == "weekly" && tick.Weekday() == time.Monday) || (p == "monthly" && tick.Day() == 1) {
		return p
	}
	return ""
}

func signedGB(n int64) string {
	if n < 0 {
		return "-" + gb(-n)
	}
	return "+" + gb(n)
}

// healthReport summarises the health of the NAS over the last week or month,
// short enough to read on a phone. Each line is a server text, translated
// into the notification language by sendNotification.
func (s *Server) healthReport(period string) (string, string) {
	host, _ := hostname()
	days := 30
	subject := "Monthly report of " + host
	if period == "weekly" {
		days, subject = 7, "Weekly report of "+host
	}
	now := time.Now()
	since := now.AddDate(0, 0, -days)
	var b strings.Builder
	raids, _ := storage.ListRaid()
	pools, _ := storage.ListZPools(false)
	vols := s.volumeSummaries(raids, pools)
	alerts := s.alerts(raids, pools, vols)

	b.WriteString("💾 Storage\n")
	for _, v := range vols {
		pct := 0.0
		if v.Total > 0 {
			pct = float64(v.Used) / float64(v.Total) * 100
		}
		if g, ok := health.Growth(s.hist.Volume(v.Name), since); ok {
			fmt.Fprintf(&b, "• %s: %s used of %s (%.0f%%), %s in %d days\n", v.Name, gb(v.Used), gb(v.Total), pct, signedGB(g), days)
		} else {
			fmt.Fprintf(&b, "• %s: %s used of %s (%.0f%%)\n", v.Name, gb(v.Used), gb(v.Total), pct)
		}
	}
	if len(vols) == 0 {
		b.WriteString("• no volume\n")
	}

	b.WriteString("\n🩺 Health\n")
	if len(alerts) == 0 {
		b.WriteString("• everything is healthy: disks, RAID arrays, ZFS pools and volumes\n")
	}
	for _, a := range alerts {
		fmt.Fprintf(&b, "• %s\n", a.Message)
	}
	disks, _ := storage.ListDisks()
	fmt.Fprintf(&b, "• %d disk(s) monitored\n", len(disks))
	for _, d := range disks {
		h := s.hist.Disk(d.Model, d.Serial, d.Path)
		if h == nil {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(d.Path, "/dev/") + " " + d.Model)
		last, first := h.Points[len(h.Points)-1], health.Since(h.Points, since)
		if first != nil {
			for _, c := range health.Trends(baselineOf(first.SmartSample), last.SmartSample) {
				if c.Field != "wear" && c.Field != "sp" {
					fmt.Fprintf(&b, "• %s: %s went from %d to %d\n", name, health.CounterName(c.Field), c.From, c.To)
				}
			}
		}
		if d.Kind == "ssd" || d.Kind == "nvme" {
			w := health.WearOf(h.Points)
			switch {
			case w.PercentUsed == nil:
			case w.RemainingDays != nil && *w.RemainingDays < 730:
				fmt.Fprintf(&b, "• %s: SSD wear %d%%, about %d months left\n", name, *w.PercentUsed, max(*w.RemainingDays/30, 1))
			default:
				fmt.Fprintf(&b, "• %s: SSD wear %d%%\n", name, *w.PercentUsed)
			}
		}
	}

	var backupsOK, backupsKO, cloudOK, cloudKO, avScans, avInfected int
	var lastFail, quotaLines []string
	var failedLogins, blocked, ransom, ups int
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
		for _, e := range st.Events {
			if e.Time.Before(since) {
				continue
			}
			switch {
			case e.Category == "auth" && e.Level == "warning":
				failedLogins++
			case e.Category == "guard" && e.Level == "warning":
				blocked++
			case e.Category == "ransomware" && e.Level == "error":
				ransom++
			case e.Category == "ups" && e.Level != "info":
				ups++
			}
		}
	})
	fmt.Fprintf(&b, "\n📦 Backups (%d days)\n", days)
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

	b.WriteString("\n🔄 Updates\n")
	if n := aptInstalledSince(since); n > 0 {
		fmt.Fprintf(&b, "• %d package(s) installed\n", n)
	}
	info := s.updateInfo(false)
	if info.Candidate != "" {
		fmt.Fprintf(&b, "• StrataOS %s is available\n", info.Candidate)
	}
	if info.System > 0 {
		fmt.Fprintf(&b, "• %d update(s) available\n", info.System)
	} else {
		b.WriteString("• the system is up to date\n")
	}

	run := s.evalAdvisor()
	fmt.Fprintf(&b, "\n🔒 Security\n• security score: %d/100\n", run.Score)
	n := 0
	for _, it := range run.Items {
		if it.Severity == "high" && !it.Ignored && n < 3 {
			fmt.Fprintf(&b, "• %s\n", it.Title)
			n++
		}
	}

	b.WriteString("\n📋 Events\n")
	if failedLogins+blocked+ransom+ups == 0 {
		b.WriteString("• nothing notable\n")
	}
	if failedLogins > 0 {
		fmt.Fprintf(&b, "• %d failed sign-in(s)\n", failedLogins)
	}
	if blocked > 0 {
		fmt.Fprintf(&b, "• %d address(es) blocked\n", blocked)
	}
	if ransom > 0 {
		fmt.Fprintf(&b, "• %d ransomware alert(s)\n", ransom)
	}
	if ups > 0 {
		fmt.Fprintf(&b, "• %d power event(s) (UPS)\n", ups)
	}
	fmt.Fprintf(&b, "\n🖥 System\n• StrataOS %s, running for %d day(s)\n", s.version, uptimeDays())
	return subject, b.String()
}

// baselineOf turns a past reading into the baseline of health.Trends, to
// list what got worse since then.
func baselineOf(p storage.SmartSample) map[string]int64 {
	b := map[string]int64{}
	for f, v := range map[string]*int64{"re": p.Reallocated, "pe": p.Pending, "un": p.Uncorrectable, "crc": p.CRCErrors, "me": p.MediaErrors} {
		if v != nil {
			b[f] = *v
		}
	}
	return b
}

// aptInstalledSince counts the packages installed or upgraded since a date
// (the apt history and its previous rotation).
func aptInstalledSince(since time.Time) int {
	n := 0
	if b, err := os.ReadFile("/var/log/apt/history.log"); err == nil {
		n += health.AptInstalled(string(b), since)
	}
	if f, err := os.Open("/var/log/apt/history.log.1.gz"); err == nil {
		defer f.Close()
		if z, err := gzip.NewReader(f); err == nil {
			if b, err := io.ReadAll(io.LimitReader(z, 64<<20)); err == nil {
				n += health.AptInstalled(string(b), since)
			}
		}
	}
	return n
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
	// Sends the report now (weekly or monthly, default: the chosen period).
	s.admin("POST /api/settings/report", func(r *http.Request) (any, error) {
		var req struct {
			Period string `json:"period"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		var lang, period string
		s.store.Read(func(st *store.State) { lang, period = st.MessageLang(), st.Settings.ReportPeriod() })
		if req.Period == "weekly" || req.Period == "monthly" {
			period = req.Period
		}
		subject, body := s.healthReport(period)
		shown := i18n.Lines(lang, body)
		if _, err := s.sendNotification("", subject, body); err != nil {
			return map[string]string{"report": shown, "warning": err.Error()}, nil
		}
		return map[string]string{"report": shown}, nil
	})
}
