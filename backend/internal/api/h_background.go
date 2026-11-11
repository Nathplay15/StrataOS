package api

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"strataos/internal/store"
)

// Long background analyses (smart photos, document search) wait while the
// NAS is busy, outside the hours the administrator allows, and shortly
// before a scheduled shutdown. Their processes run at the lowest priority
// with a limited number of threads.

var hoursRe = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)-([01]\d|2[0-3]):([0-5]\d)$`)

// validLimits checks background limits typed by an administrator.
func validLimits(l store.BackgroundLimits) error {
	if l.Hours != "" && !hoursRe.MatchString(l.Hours) {
		return badRequest("hours must be like 01:00-07:00")
	}
	if l.Threads < 0 || l.Threads > runtime.NumCPU() {
		return badRequest(fmt.Sprintf("threads must be between 0 and %d", runtime.NumCPU()))
	}
	return nil
}

// threadsOf is the number of threads background work may use.
func threadsOf(l store.BackgroundLimits) int {
	if l.Threads > 0 {
		return l.Threads
	}
	return max(1, runtime.NumCPU()/4)
}

// inHours tells whether now is within "HH:MM-HH:MM" (which may cross midnight).
func inHours(hours string, now time.Time) bool {
	m := hoursRe.FindStringSubmatch(hours)
	if m == nil {
		return true
	}
	min := func(h, mm string) int { a, _ := strconv.Atoi(h); b, _ := strconv.Atoi(mm); return a*60 + b }
	from, to, cur := min(m[1], m[2]), min(m[3], m[4]), now.Hour()*60+now.Minute()
	if from <= to {
		return cur >= from && cur < to
	}
	return cur >= from || cur < to
}

// backgroundWait returns why background work must wait now ("" when it may
// run): "hours" (outside the allowed hours), "shutdown" (scheduled shutdown
// in less than 15 minutes), "busy" (the load, minus our own threads, above
// 70 % of the cores).
func backgroundWait(l store.BackgroundLimits, now time.Time, load float64, cores, own int, shutdown time.Time) string {
	switch {
	case !inHours(l.Hours, now):
		return "hours"
	case !shutdown.IsZero() && shutdown.Sub(now) < 15*time.Minute:
		return "shutdown"
	case !l.AnyLoad && load-float64(own) > 0.7*float64(cores):
		return "busy"
	}
	return ""
}

func loadAverage() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// backgroundGate blocks while limits() say to wait, reporting the reason.
// own() is the number of threads of ours counted in the load.
func (s *Server) backgroundGate(limits func() store.BackgroundLimits, own func() int) func(ctx context.Context, wait func(string)) error {
	return func(ctx context.Context, wait func(string)) error {
		for {
			var sched time.Time
			s.store.Read(func(st *store.State) { sched = st.Energy.Schedule.NextShutdown(time.Now()) })
			reason := backgroundWait(limits(), time.Now(), loadAverage(), runtime.NumCPU(), own(), sched)
			if reason == "" {
				return nil
			}
			wait(reason)
			t := time.NewTimer(30 * time.Second)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
	}
}
