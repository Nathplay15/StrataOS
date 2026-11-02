package system

import "testing"

func TestTimezoneFromLink(t *testing.T) {
	for l, want := range map[string]string{
		"/usr/share/zoneinfo/Europe/Paris":         "Europe/Paris",
		"../usr/share/zoneinfo/Europe/Paris":       "Europe/Paris",
		"/usr/share/zoneinfo/UTC":                  "UTC",
		"/usr/share/zoneinfo/posix/America/Denver": "America/Denver",
		"/var/db/timezone/zoneinfo/Asia/Tokyo":     "Asia/Tokyo",
		"/etc/some/file":                           "",
	} {
		if got := timezoneFromLink(l); got != want {
			t.Errorf("%s: %q, want %q", l, got, want)
		}
	}
}
