package api

// Fleet health dashboard: what each NAS reports about itself (fleetHealth,
// GET /api/fleet/peer/health) and the pure part of the console's view: the
// reasons a NAS needs attention, its level, and the fleet summary. Kept apart
// from the HTTP calls to test it.

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type fleetHealth struct {
	Hostname   string           `json:"hostname"`
	Version    string           `json:"version"`
	UptimeDays int              `json:"uptimeDays"`
	Updates    *fleetUpdateInfo `json:"updates,omitempty"`
	Advisor    *fleetAdvisor    `json:"advisor,omitempty"`
	Disks      fleetDisks       `json:"disks"`
	Arrays     fleetArrays      `json:"arrays"`
	Space      fleetSpace       `json:"space"`
	Backups    fleetBackups     `json:"backups"`
	Temps      fleetTemps       `json:"temps"`
	Alerts     fleetAlerts      `json:"alerts"`
	Access     *fleetAccess     `json:"access,omitempty"`
	// Partial: an older StrataOS without this endpoint (built from its summary).
	Partial bool      `json:"partial,omitempty"`
	At      time.Time `json:"at"`
}

type fleetAdvisor struct {
	Score  int       `json:"score"`
	High   int       `json:"high"`
	Medium int       `json:"medium"`
	Time   time.Time `json:"time"`
}

type fleetDisks struct {
	Total   int `json:"total"`
	Failing int `json:"failing"` // SMART overall test failed
	Warning int `json:"warning"` // early signs of wear
	// MaxWear: highest percentage of rated endurance used by an SSD.
	MaxWear *int64   `json:"maxWear,omitempty"`
	Issues  []string `json:"issues,omitempty"`
}

type fleetArrays struct {
	Total      int      `json:"total"` // RAID arrays and ZFS pools
	Degraded   int      `json:"degraded"`
	Rebuilding int      `json:"rebuilding"`
	Issues     []string `json:"issues,omitempty"`
}

type fleetVol struct {
	Name   string `json:"name"`
	Total  int64  `json:"total"`
	Used   int64  `json:"used"`
	Health string `json:"health"`
}

type fleetSpace struct {
	Total      int64      `json:"total"`
	Used       int64      `json:"used"`
	Fullest    string     `json:"fullest,omitempty"`
	FullestPct float64    `json:"fullestPct"`
	Unmounted  int        `json:"unmounted"`
	Volumes    []fleetVol `json:"volumes"`
}

type fleetBackups struct {
	Total           int        `json:"total"`
	Failed          int        `json:"failed"` // last run failed
	LastSuccess     *time.Time `json:"lastSuccess,omitempty"`
	LastFailure     *time.Time `json:"lastFailure,omitempty"`
	LastFailureName string     `json:"lastFailureName,omitempty"`
	// Stale: enabled tasks without a success for fleetBackupStale.
	Stale []string `json:"stale,omitempty"`
}

type fleetTemps struct {
	CPU      *float64 `json:"cpu,omitempty"`
	Disk     *int64   `json:"disk,omitempty"` // hottest awake disk
	DiskName string   `json:"diskName,omitempty"`
}

type fleetAlertMsg struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

type fleetAlerts struct {
	Errors   int             `json:"errors"`
	Warnings int             `json:"warnings"`
	Messages []fleetAlertMsg `json:"messages,omitempty"` // the first ones
}

// Thresholds of the dashboard.
const (
	fleetBackupStale = 8 * 24 * time.Hour
	fleetFullPct     = 90
	fleetWearPct     = 90
	fleetCPUHot      = 85
	fleetDiskHot     = 55
	fleetAdvisorLow  = 60
)

// Attention levels, by increasing severity.
const (
	levelOK       = "ok"
	levelInfo     = "info"
	levelWarning  = "warning"
	levelCritical = "critical"
)

func healthRank(l string) int {
	switch l {
	case levelInfo:
		return 1
	case levelWarning:
		return 2
	case levelCritical:
		return 3
	}
	return 0
}

// fleetReason is why a NAS shows in the dashboard summary. Text is an
// English server text; Kind tells the interface which column it concerns.
type fleetReason struct {
	Level string `json:"level"`
	Kind  string `json:"kind"` // online | disks | arrays | space | backups | security | temps | alerts | updates | version
	Text  string `json:"text"`
}

// fleetReasons lists what needs attention on a NAS (h nil: unreachable).
func fleetReasons(online bool, h *fleetHealth) []fleetReason {
	if !online || h == nil {
		return []fleetReason{{levelCritical, "online", "This NAS does not answer"}}
	}
	var out []fleetReason
	add := func(level, kind, f string, a ...any) {
		out = append(out, fleetReason{level, kind, fmt.Sprintf(f, a...)})
	}
	if h.Disks.Failing > 0 {
		add(levelCritical, "disks", "%d disk(s) failing", h.Disks.Failing)
	}
	if h.Arrays.Degraded > 0 {
		add(levelCritical, "arrays", "%d degraded or failed array(s)", h.Arrays.Degraded)
	}
	if h.Disks.Failing == 0 && h.Arrays.Degraded == 0 && h.Alerts.Errors > 0 {
		add(levelCritical, "alerts", "%d error alert(s)", h.Alerts.Errors)
	}
	if h.Backups.Failed > 0 {
		add(levelWarning, "backups", "%d backup task(s) failed", h.Backups.Failed)
	}
	if n := len(h.Backups.Stale); n > 0 {
		add(levelWarning, "backups", "%d backup task(s) without success for 8 days", n)
	}
	if h.Disks.Warning > 0 {
		add(levelWarning, "disks", "%d disk(s) with SMART warnings", h.Disks.Warning)
	}
	if h.Disks.MaxWear != nil && *h.Disks.MaxWear >= fleetWearPct {
		add(levelWarning, "disks", "An SSD has used %d%% of its endurance", *h.Disks.MaxWear)
	}
	if h.Arrays.Rebuilding > 0 {
		add(levelInfo, "arrays", "%d array(s) rebuilding", h.Arrays.Rebuilding)
	}
	if h.Space.FullestPct >= fleetFullPct {
		add(levelWarning, "space", "Volume %s is %.0f%% full", h.Space.Fullest, h.Space.FullestPct)
	}
	if h.Space.Unmounted > 0 {
		add(levelWarning, "space", "%d volume(s) not mounted", h.Space.Unmounted)
	}
	if a := h.Advisor; a != nil {
		switch {
		case a.High > 0:
			add(levelWarning, "security", "%d high-risk security finding(s)", a.High)
		case a.Score < fleetAdvisorLow:
			add(levelWarning, "security", "Security score %d/100", a.Score)
		}
	}
	if t := h.Temps.CPU; t != nil && *t >= fleetCPUHot {
		add(levelWarning, "temps", "Processor at %.0f °C", *t)
	}
	if t := h.Temps.Disk; t != nil && *t >= fleetDiskHot {
		add(levelWarning, "temps", "Disk %s at %d °C", h.Temps.DiskName, *t)
	}
	if u := h.Updates; u != nil {
		if u.Candidate != "" {
			add(levelInfo, "updates", "StrataOS %s available", u.Candidate)
		}
		if u.RebootRequired {
			add(levelInfo, "updates", "Restart required")
		}
	}
	if h.Partial {
		add(levelInfo, "version", "Older StrataOS: partial information")
	}
	return out
}

// fleetLevel is the highest level of the reasons ("ok" without any).
func fleetLevel(rs []fleetReason) string {
	l := levelOK
	for _, r := range rs {
		if healthRank(r.Level) > healthRank(l) {
			l = r.Level
		}
	}
	return l
}

// fleetHealthTotals is the line above the dashboard.
type fleetHealthTotals struct {
	Total     int `json:"total"`
	Online    int `json:"online"`
	Offline   int `json:"offline"`
	Critical  int `json:"critical"`
	Warning   int `json:"warning"`
	Attention int `json:"attention"` // critical + warning
	Updates   int `json:"updates"`   // a StrataOS or system update is waiting
	// Space of every reachable NAS.
	SpaceTotal int64 `json:"spaceTotal"`
	SpaceUsed  int64 `json:"spaceUsed"`
}

type fleetHealthItem struct {
	Online bool
	Level  string
	Health *fleetHealth
}

func fleetHealthSum(items []fleetHealthItem) fleetHealthTotals {
	var t fleetHealthTotals
	for _, it := range items {
		t.Total++
		if it.Online {
			t.Online++
		} else {
			t.Offline++
		}
		switch it.Level {
		case levelCritical:
			t.Critical++
		case levelWarning:
			t.Warning++
		}
		if h := it.Health; h != nil && it.Online {
			if u := h.Updates; u != nil && (u.Candidate != "" || u.System > 0) {
				t.Updates++
			}
			t.SpaceTotal += h.Space.Total
			t.SpaceUsed += h.Space.Used
		}
	}
	t.Attention = t.Critical + t.Warning
	return t
}

// fleetSpaceOf sums the volumes and finds the fullest one.
func fleetSpaceOf(vols []fleetVol) fleetSpace {
	sp := fleetSpace{Volumes: vols}
	if sp.Volumes == nil {
		sp.Volumes = []fleetVol{}
	}
	for _, v := range vols {
		if v.Health == "unmounted" {
			sp.Unmounted++
			continue
		}
		sp.Total += v.Total
		sp.Used += v.Used
		if v.Total > 0 {
			if p := 100 * float64(v.Used) / float64(v.Total); p > sp.FullestPct {
				sp.FullestPct, sp.Fullest = math.Round(p*10)/10, v.Name
			}
		}
	}
	return sp
}

// fleetFromSummary builds a partial health report from the summary of an
// older StrataOS (GET /api/fleet/peer/summary).
func fleetFromSummary(sum map[string]any) *fleetHealth {
	h := &fleetHealth{Partial: true}
	h.Hostname, _ = sum["hostname"].(string)
	h.Version, _ = sum["version"].(string)
	if d, ok := sum["uptimeDays"].(float64); ok {
		h.UptimeDays = int(d)
	}
	var vols []fleetVol
	if list, ok := sum["volumes"].([]any); ok {
		for _, x := range list {
			v, _ := x.(map[string]any)
			if v == nil {
				continue
			}
			fv := fleetVol{}
			fv.Name, _ = v["name"].(string)
			fv.Health, _ = v["health"].(string)
			if f, ok := v["total"].(float64); ok {
				fv.Total = int64(f)
			}
			if f, ok := v["used"].(float64); ok {
				fv.Used = int64(f)
			}
			if fv.Health != "healthy" && fv.Health != "unmounted" && fv.Health != "" {
				h.Arrays.Degraded++
			}
			vols = append(vols, fv)
		}
	}
	h.Space = fleetSpaceOf(vols)
	if n, ok := sum["raids"].(float64); ok {
		h.Arrays.Total += int(n)
	}
	if n, ok := sum["pools"].(float64); ok {
		h.Arrays.Total += int(n)
	}
	if n, ok := sum["disks"].(float64); ok {
		h.Disks.Total = int(n)
	}
	if b, ok := sum["backups"].(map[string]any); ok {
		ok2, _ := b["ok"].(float64)
		failed, _ := b["failed"].(float64)
		h.Backups.Total, h.Backups.Failed = int(ok2+failed), int(failed)
		h.Backups.LastFailureName, _ = b["lastFailure"].(string)
	}
	if list, ok := sum["alerts"].([]any); ok {
		for _, x := range list {
			a, _ := x.(map[string]any)
			if a == nil {
				continue
			}
			m := fleetAlertMsg{}
			m.Level, _ = a["level"].(string)
			m.Message, _ = a["message"].(string)
			switch m.Level {
			case "error":
				h.Alerts.Errors++
				if strings.HasPrefix(m.Message, "Disk ") && strings.Contains(m.Message, "fails its SMART") {
					h.Disks.Failing++
				}
			case "warning":
				h.Alerts.Warnings++
				if strings.HasPrefix(m.Message, "Disk ") && strings.Contains(m.Message, "getting weak") {
					h.Disks.Warning++
				}
			}
			if len(h.Alerts.Messages) < 6 {
				h.Alerts.Messages = append(h.Alerts.Messages, m)
			}
		}
	}
	if u, ok := sum["updates"].(map[string]any); ok {
		var info fleetUpdateInfo
		info.Version, _ = u["version"].(string)
		info.Candidate, _ = u["candidate"].(string)
		info.RebootRequired, _ = u["rebootRequired"].(bool)
		if n, ok := u["system"].(float64); ok {
			info.System = int(n)
		}
		h.Updates = &info
	}
	if a, ok := sum["access"].(map[string]any); ok {
		var acc fleetAccess
		acc.Updates, _ = a["updates"].(bool)
		acc.Reboot, _ = a["reboot"].(bool)
		h.Access = &acc
	}
	return h
}
