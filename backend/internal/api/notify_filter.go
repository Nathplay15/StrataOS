package api

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"strataos/internal/store"
)

// Notification levels, from the least to the most important. An empty level
// marks an explicit send (test, monthly report): only the channel switch applies.
var levelRank = map[string]int{"info": 0, "warning": 1, "error": 2}

// validLevel accepts the levels a channel minimum can take ("" = all).
func validLevel(l string) bool {
	_, ok := levelRank[l]
	return ok || l == ""
}

// channelAccepts tells whether a channel sends a notification of this level.
func channelAccepts(c store.NotifyChannel, level string) bool {
	if c.Disabled {
		return false
	}
	return level == "" || levelRank[level] >= levelRank[c.MinLevel]
}

var hhmmRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func minutesOf(hhmm string) (int, bool) {
	if !hhmmRe.MatchString(hhmm) {
		return 0, false
	}
	h, _ := strconv.Atoi(hhmm[:2])
	m, _ := strconv.Atoi(hhmm[3:])
	return h*60 + m, true
}

// validQuietHours checks the window when it is enabled.
func validQuietHours(q store.QuietHours) error {
	if !q.Enabled {
		return nil
	}
	s, ok1 := minutesOf(q.Start)
	e, ok2 := minutesOf(q.End)
	if !ok1 || !ok2 {
		return badRequest("quiet hours: times must be HH:MM")
	}
	if s == e {
		return badRequest("quiet hours: start and end must differ")
	}
	return nil
}

// inQuietHours tells whether t (local time) falls in the window [start, end),
// which crosses midnight when end is before start (22:00 → 07:00).
func inQuietHours(q store.QuietHours, t time.Time) bool {
	if !q.Enabled {
		return false
	}
	s, ok1 := minutesOf(q.Start)
	e, ok2 := minutesOf(q.End)
	if !ok1 || !ok2 || s == e {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	if s < e {
		return m >= s && m < e
	}
	return m >= s || m < e
}

// quietAllows applies quiet hours: only errors go through, or nothing with
// SilenceAll. Explicit sends (level "") are never held back.
func quietAllows(q store.QuietHours, level string, t time.Time) bool {
	if level == "" || !inQuietHours(q, t) {
		return true
	}
	return level == "error" && !q.SilenceAll
}

// validNotifyPrefs checks the per-channel levels and the quiet hours.
func validNotifyPrefs(set store.Settings) error {
	c := set.NotifyChannels
	for _, l := range []string{c.Email.MinLevel, c.Telegram.MinLevel, c.Ntfy.MinLevel, c.Webhook.MinLevel, c.Mobile.MinLevel} {
		if !validLevel(l) {
			return badRequest(fmt.Sprintf("invalid notification level %q", l))
		}
	}
	return validQuietHours(set.QuietHours)
}
