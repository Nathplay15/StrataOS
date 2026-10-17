package api

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"strataos/internal/shares"
	"strataos/internal/store"
	"strataos/internal/sys"
)

var shareQuotaMu sync.Mutex

// folderSize measures a folder (one file system, low priority).
func folderSize(ctx context.Context, path string) (int64, error) {
	out, err := sys.RunOpts(ctx, sys.Opts{Timeout: 30 * time.Minute}, "nice", "-n", "19", "ionice", "-c3", "du", "-sbx", path)
	if err != nil {
		return 0, err
	}
	f := strings.Fields(out)
	if len(f) == 0 {
		return 0, fmt.Errorf("du: no output")
	}
	return strconv.ParseInt(f[0], 10, 64)
}

// checkShareQuotas measures the shares that have a quota, alerts at 90 % and
// 100 % and makes full shares read-only when asked.
func (s *Server) checkShareQuotas() {
	shareQuotaMu.Lock()
	defer shareQuotaMu.Unlock()
	var list []store.Share
	s.store.Read(func(st *store.State) {
		for _, sh := range st.Shares {
			if sh.QuotaGB > 0 {
				list = append(list, *sh)
			}
		}
	})
	changed := false
	for _, sh := range list {
		used, err := folderSize(context.Background(), sh.Path)
		if err != nil {
			slog.Warn("share quota", "share", sh.Name, "err", err)
			continue
		}
		limit := sh.QuotaGB * 1e9
		pct := float64(used) / limit * 100
		prev := float64(sh.Used) / limit * 100
		full := sh.QuotaBlock && float64(used) >= limit
		switch {
		case pct >= 100 && prev < 100:
			msg := fmt.Sprintf("Le dossier partagé %s a atteint son quota (%.1f Go sur %.1f Go)", sh.Name, float64(used)/1e9, sh.QuotaGB)
			if sh.QuotaBlock {
				msg += " : il passe en lecture seule jusqu'à ce que de la place soit libérée"
			}
			s.store.Log("error", "quota", "", msg)
			s.notify("Quota atteint", msg)
		case pct >= 90 && prev < 90:
			msg := fmt.Sprintf("Le dossier partagé %s est plein à %.0f %% de son quota (%.1f Go sur %.1f Go)", sh.Name, pct, float64(used)/1e9, sh.QuotaGB)
			s.store.Log("warning", "quota", "", msg)
			s.notify("Quota presque atteint", msg)
		case !full && sh.QuotaFull:
			s.store.Log("info", "quota", "", fmt.Sprintf("Le dossier partagé %s est de nouveau sous son quota : écriture rétablie", sh.Name))
		}
		_ = s.store.Update(func(st *store.State) error {
			if x := st.Share(sh.Name); x != nil {
				x.Used, x.UsedAt = used, time.Now()
				if x.QuotaFull != full {
					x.QuotaFull = full
					changed = true
					c := *x
					if err := shares.Prepare(&c); err != nil {
						slog.Warn("share quota", "share", sh.Name, "err", err)
					}
				}
			}
			return nil
		})
	}
	if changed {
		_ = s.applyShares()
	}
}

func (s *Server) shareQuotaLoop() {
	time.Sleep(3 * time.Minute)
	for {
		s.checkShareQuotas()
		time.Sleep(30 * time.Minute)
	}
}
