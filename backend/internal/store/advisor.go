package store

import "time"

// Advisor is the state of the security advisor: the items the
// administrator chose to ignore and the last run.
type Advisor struct {
	Ignored []string    `json:"ignored,omitempty"`
	Last    *AdvisorRun `json:"last,omitempty"`
}

// AdvisorRun is the result of one run of the checks.
type AdvisorRun struct {
	Time  time.Time     `json:"time"`
	Score int           `json:"score"`
	Items []AdvisorItem `json:"items"`
}

// AdvisorItem is a finding of the security advisor. Title and Detail are
// English server texts (translated by the interface and notifications).
type AdvisorItem struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // high | medium | low | info
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	// Fix names the one-click fix (POST /api/security/advisor/fix/{id}),
	// Link the page of the setting.
	Fix     string `json:"fix,omitempty"`
	Link    string `json:"link,omitempty"`
	Ignored bool   `json:"ignored,omitempty"`
}

// ReportPeriod is the period of the health report: "weekly", "monthly" or
// "" (none).
func (s Settings) ReportPeriod() string {
	switch s.Report {
	case "weekly", "monthly":
		return s.Report
	case "":
		if s.MonthlyReport {
			return "monthly"
		}
	}
	return ""
}
