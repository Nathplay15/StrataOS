package health

import (
	"strings"
	"time"
)

// AptInstalled counts the packages upgraded or installed since a date in
// the text of /var/log/apt/history.log (dates in local time).
func AptInstalled(log string, since time.Time) int {
	n := 0
	in := false
	for _, l := range strings.Split(log, "\n") {
		k, v, ok := strings.Cut(l, ": ")
		if !ok {
			continue
		}
		switch k {
		case "Start-Date":
			t, err := time.ParseInLocation("2006-01-02  15:04:05", strings.TrimSpace(v), time.Local)
			in = err == nil && !t.Before(since)
		case "Upgrade", "Install":
			if in {
				// One "(versions)" group per package.
				n += strings.Count(v, "(")
			}
		}
	}
	return n
}
