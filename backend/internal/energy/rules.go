package energy

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"strataos/internal/sys"
)

// Power actions of a rule.
const (
	ActShutdown = "shutdown"
	ActRestart  = "restart"
	ActWake     = "wake"
)

// PowerRule is one scheduled power action, at a local time on some days.
type PowerRule struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Action  string `json:"action"` // shutdown, restart, wake
	Days    []int  `json:"days"`   // 0 = Sunday … 6 = Saturday
	Time    string `json:"time"`   // HH:MM
}

// PowerGuard says what delays a scheduled shutdown or restart. Running jobs
// (backups, scrubs, rebuilds, migrations, updates…) always do.
type PowerGuard struct {
	// Users: also wait while someone uses the NAS (files open over SMB,
	// a shell session, a video being played).
	Users bool `json:"users,omitempty"`
	// RetryMin between checks (default 10), MaxMin of delay before the
	// action is skipped for this time (default 120).
	RetryMin int `json:"retryMin,omitempty"`
	MaxMin   int `json:"maxMin,omitempty"`
	// WarnMin: a notification this many minutes before (default 10, -1: none).
	WarnMin int `json:"warnMin,omitempty"`
}

func (g PowerGuard) retry() time.Duration {
	if g.RetryMin <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(g.RetryMin) * time.Minute
}

func (g PowerGuard) max() time.Duration {
	if g.MaxMin <= 0 {
		return 120 * time.Minute
	}
	return time.Duration(g.MaxMin) * time.Minute
}

// Warn is how long before an action users are told (0: never).
func (g PowerGuard) Warn() time.Duration {
	switch {
	case g.WarnMin < 0:
		return 0
	case g.WarnMin == 0:
		return 10 * time.Minute
	}
	return time.Duration(g.WarnMin) * time.Minute
}

// Validate checks the delays.
func (g PowerGuard) Validate() error {
	if g.RetryMin < 0 || g.RetryMin > 120 || g.MaxMin < 0 || g.MaxMin > 24*60 || g.WarnMin < -1 || g.WarnMin > 120 {
		return fmt.Errorf("invalid postponement delays")
	}
	if g.MaxMin > 0 && g.MaxMin < g.RetryMin {
		return fmt.Errorf("the longest delay must not be shorter than the retry delay")
	}
	return nil
}

// ValidateRules checks a list of rules (at most 50).
func ValidateRules(rules []PowerRule) error {
	if len(rules) > 50 {
		return fmt.Errorf("too many power rules")
	}
	ids := map[string]bool{}
	for _, r := range rules {
		if r.ID == "" || len(r.ID) > 40 || strings.ContainsAny(r.ID, " /\n") || ids[r.ID] {
			return fmt.Errorf("invalid rule identifier")
		}
		ids[r.ID] = true
		if r.Action != ActShutdown && r.Action != ActRestart && r.Action != ActWake {
			return fmt.Errorf("unknown power action %q", r.Action)
		}
		if !hhmm.MatchString(r.Time) {
			return fmt.Errorf("times must be HH:MM")
		}
		if len(r.Days) == 0 {
			return fmt.Errorf("choose at least one day")
		}
		for _, d := range r.Days {
			if d < 0 || d > 6 {
				return fmt.Errorf("invalid day")
			}
		}
	}
	return nil
}

// Next returns the first time strictly after `after` the rule fires, in
// after's location (zero when it never does). On the day clocks go forward
// a time in the skipped hour fires once, an hour later on the wall clock;
// on the day they go back a time of the repeated hour fires once (the
// first one: the next search starts after it).
func (r PowerRule) Next(after time.Time) time.Time {
	if !r.Enabled || !hhmm.MatchString(r.Time) {
		return time.Time{}
	}
	h, _ := strconv.Atoi(r.Time[:2])
	m, _ := strconv.Atoi(r.Time[3:])
	y, mo, d := after.Date()
	for i := 0; i < 8; i++ {
		day := time.Date(y, mo, d+i, 12, 0, 0, 0, after.Location())
		if !slices.Contains(r.Days, int(day.Weekday())) {
			continue
		}
		t := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, after.Location())
		if t.After(after) {
			return t
		}
	}
	return time.Time{}
}

// NextAction returns the first shutdown or restart after `after`; at the
// same moment a shutdown wins over a restart.
func NextAction(rules []PowerRule, after time.Time) (time.Time, PowerRule) {
	var best time.Time
	var rule PowerRule
	for _, r := range rules {
		if r.Action == ActWake {
			continue
		}
		t := r.Next(after)
		if t.IsZero() {
			continue
		}
		if best.IsZero() || t.Before(best) || (t.Equal(best) && r.Action == ActShutdown && rule.Action != ActShutdown) {
			best, rule = t, r
		}
	}
	return best, rule
}

// NextWake returns the first power-on time after `after` (zero: none).
func NextWake(rules []PowerRule, after time.Time) time.Time {
	var best time.Time
	for _, r := range rules {
		if r.Action != ActWake {
			continue
		}
		if t := r.Next(after); !t.IsZero() && (best.IsZero() || t.Before(best)) {
			best = t
		}
	}
	return best
}

// MinWakeGap: the RTC alarm of a power-on is only useful once the machine is
// off; a power-on closer than this to the shutdown waits for the next one.
const MinWakeGap = 3 * time.Minute

// WakeAfterShutdown is when the NAS powers on again after a shutdown at t.
func WakeAfterShutdown(rules []PowerRule, t time.Time) time.Time {
	return NextWake(rules, t.Add(MinWakeGap-time.Second))
}

// Verdict of a due action.
const (
	Run      = "run"
	Postpone = "postpone"
	Skip     = "skip"
)

// Decide says what to do with an action planned at due, now, when job names
// a running task and user someone using the NAS ("" for none): run it,
// postpone it by the retry delay, or skip it when it would be later than
// the longest delay allowed.
func Decide(g PowerGuard, due, now time.Time, job, user string) (verdict, reason string) {
	reason = job
	if reason == "" && g.Users {
		reason = user
	}
	if reason == "" {
		return Run, ""
	}
	if now.Add(g.retry()).Sub(due) <= g.max() {
		return Postpone, reason
	}
	return Skip, reason
}

// RetryDelay is the delay between two checks of a postponed action.
func (g PowerGuard) RetryDelay() time.Duration { return g.retry() }

// MigrateSchedule turns the single schedule of 0.11 into rules: a shutdown
// on its days and, when set, a power-on at the wake time that follows each
// of them (the same day when later, the next day otherwise).
func MigrateSchedule(s Schedule) []PowerRule {
	if !s.Enabled || s.Validate() != nil {
		return nil
	}
	days := slices.Clone(s.Days)
	slices.Sort(days)
	out := []PowerRule{{ID: "shutdown", Enabled: true, Action: ActShutdown, Days: days, Time: s.Shutdown}}
	if s.Wake == "" {
		return out
	}
	var wd []int
	for _, d := range days {
		if s.Wake > s.Shutdown {
			wd = append(wd, d)
		} else {
			wd = append(wd, (d+1)%7)
		}
	}
	slices.Sort(wd)
	return append(out, PowerRule{ID: "wake", Enabled: true, Action: ActWake, Days: slices.Compact(wd), Time: s.Wake})
}

// ---- RTC wake alarm ----

// RTCAlarm reads the wake alarm programmed in the clock (zero: none).
func RTCAlarm() time.Time {
	b, err := readSys("sys/class/rtc/rtc0/wakealarm")
	if err != nil || b == "" {
		return time.Time{}
	}
	n, err := strconv.ParseInt(b, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// SetRTCAlarm programs the clock to power the machine on at t (zero clears
// the alarm). rtcwake reads /etc/adjtime: a clock kept in local time works.
func SetRTCAlarm(t time.Time) error {
	if !RTCWakeSupported() {
		return fmt.Errorf("this machine's clock cannot power it on")
	}
	if t.IsZero() {
		_, err := sys.Run("rtcwake", "-m", "disable")
		return err
	}
	_, err := sys.Run("rtcwake", "-m", "no", "-t", strconv.FormatInt(t.Unix(), 10))
	return err
}

// Restart reboots now.
func Restart() error {
	_, err := sys.Run("systemctl", "reboot")
	return err
}
