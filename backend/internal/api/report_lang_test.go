package api

import (
	"strings"
	"testing"

	"strataos/internal/i18n"
)

// Every line healthReport can write must be a known server text, or the
// report goes out half in English (keep this sample in step with report.go).
func TestReportLinesTranslated(t *testing.T) {
	body := `Weekly report of nas
Monthly report of nas
💾 Storage
• data: 1.0 GB used of 2.0 GB (50%), +1.0 GB in 7 days
• data: 1.0 GB used of 2.0 GB (50%)
• no volume
🩺 Health
• everything is healthy: disks, RAID arrays, ZFS pools and volumes
• 3 disk(s) monitored
• sda WD: reallocated sectors went from 0 to 2
• sda X: SSD wear 12%, about 5 months left
• sda X: SSD wear 12%
📦 Backups (7 days)
• no backup ran: consider scheduling one
• 3 succeeded, 1 failed (a, b)
• 3 succeeded, 1 failed
• cloud sync: 1 succeeded, 0 failed
• antivirus: 2 scan(s), 0 with infected files
Shared folder quotas
• s: 1 GB of 2 GB
🔄 Updates
• 4 package(s) installed
• StrataOS 0.10 is available
• 3 update(s) available
• the system is up to date
🔒 Security
• security score: 41/100
• No backup task
📋 Events
• nothing notable
• 2 failed sign-in(s)
• 2 address(es) blocked
• 1 ransomware alert(s)
• 1 power event(s) (UPS)`
	fr := strings.Split(i18n.Lines("fr", body), "\n")
	for i, l := range strings.Split(body, "\n") {
		if fr[i] == l {
			t.Errorf("not translated: %q", l)
		}
	}
}
