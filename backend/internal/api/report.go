package api

import (
	"fmt"
	"net/http"
	"os"
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
		return fmt.Sprintf("%.2f To", float64(n)/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.1f Go", float64(n)/1e9)
	default:
		return fmt.Sprintf("%.0f Mo", float64(n)/1e6)
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
	subject := "Rapport mensuel de " + host
	raids, _ := storage.ListRaid()
	pools, _ := storage.ListZPools(false)
	vols := s.volumeSummaries(raids, pools)
	alerts := s.alerts(raids, pools, vols)

	b.WriteString("Stockage\n")
	for _, v := range vols {
		pct := 0.0
		if v.Total > 0 {
			pct = float64(v.Used) / float64(v.Total) * 100
		}
		fmt.Fprintf(&b, "• %s : %s utilisés sur %s (%.0f %%)\n", v.Name, gb(v.Used), gb(v.Total), pct)
	}
	if len(vols) == 0 {
		b.WriteString("• aucun volume\n")
	}

	b.WriteString("\nSanté\n")
	if len(alerts) == 0 {
		b.WriteString("• tout est sain : disques, grappes RAID, pools ZFS et volumes\n")
	}
	for _, a := range alerts {
		fmt.Fprintf(&b, "• %s\n", a.Message)
	}
	disks, _ := storage.ListDisks()
	fmt.Fprintf(&b, "• %d disque(s) surveillé(s)\n", len(disks))

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
				quotaLines = append(quotaLines, fmt.Sprintf("• %s : %s sur %.0f Go", sh.Name, gb(sh.Used), sh.QuotaGB))
			}
		}
	})
	b.WriteString("\nSauvegardes (30 jours)\n")
	if backupsOK+backupsKO == 0 {
		b.WriteString("• aucune sauvegarde exécutée : pensez à en programmer une\n")
	} else {
		fmt.Fprintf(&b, "• %d réussie(s), %d échec(s)", backupsOK, backupsKO)
		if len(lastFail) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(uniq(lastFail), ", "))
		}
		b.WriteString("\n")
	}
	if cloudOK+cloudKO > 0 {
		fmt.Fprintf(&b, "• synchronisation cloud : %d réussie(s), %d échec(s)\n", cloudOK, cloudKO)
	}
	if avScans > 0 {
		fmt.Fprintf(&b, "• antivirus : %d analyse(s), %d avec des fichiers infectés\n", avScans, avInfected)
	}
	if len(quotaLines) > 0 {
		b.WriteString("\nQuotas des dossiers partagés\n" + strings.Join(quotaLines, "\n") + "\n")
	}
	b.WriteString("\nSystème\n")
	if out, err := sys.Run("apt-get", "-s", "-o", "Debug::NoLocking=1", "upgrade"); err == nil {
		n := strings.Count(out, "\nInst ")
		if strings.HasPrefix(out, "Inst ") {
			n++
		}
		if n > 0 {
			fmt.Fprintf(&b, "• %d mise(s) à jour disponible(s)\n", n)
		} else {
			b.WriteString("• le système est à jour\n")
		}
	}
	fmt.Fprintf(&b, "• StrataOS %s, en marche depuis %d jour(s)\n", s.version, uptimeDays())
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
		if err := s.sendNotification(subject, body); err != nil {
			return map[string]string{"report": body, "warning": err.Error()}, nil
		}
		return map[string]string{"report": body}, nil
	})
}
