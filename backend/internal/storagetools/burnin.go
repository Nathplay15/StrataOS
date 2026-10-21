package storagetools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// BurnInOpts describes a burn-in test.
type BurnInOpts struct {
	Device    string
	Write     bool // destructive badblocks -w (erases the disk)
	ShortTest bool
	LongTest  bool
	// Blocks > 0 limits badblocks to the first Blocks blocks (end-to-end tests).
	Blocks int64
	Size   int64
}

// BurnInStatus is the live state of a running test.
type BurnInStatus struct {
	JobID    string    `json:"jobId"`
	Device   string    `json:"device"`
	Serial   string    `json:"serial"`
	Step     string    `json:"step"`
	Progress float64   `json:"progress"` // whole test, 0..100
	StepPct  float64   `json:"stepProgress"`
	ETA      int64     `json:"eta"` // seconds left (estimate), 0 = unknown
	Errors   int       `json:"errors"`
	Started  time.Time `json:"started"`
}

// BurnIn runs one test and publishes its progress.
type BurnIn struct {
	mu     sync.Mutex
	status BurnInStatus
	o      BurnInOpts
	// Estimated duration of each step (seconds), refined while running.
	est     map[string]float64
	order   []string
	logf    func(string, ...any)
	setProg func(float64)
}

// badblocks block size: 4 KiB, 8 KiB beyond 16 TiB (badblocks counts blocks in 32 bits).
func blockSize(size int64) int64 {
	if size > (1<<32-1)*4096 {
		return 8192
	}
	return 4096
}

// assumed sequential speed (bytes/s) for the first estimate of badblocks.
const assumedSpeed = 150e6

// NewBurnIn prepares a test. logf and setProgress report to the job.
func NewBurnIn(o BurnInOpts, serial, jobID string, logf func(string, ...any), setProgress func(float64)) *BurnIn {
	b := &BurnIn{o: o, est: map[string]float64{}, logf: logf, setProg: setProgress}
	b.status = BurnInStatus{JobID: jobID, Device: o.Device, Serial: serial, Started: time.Now()}
	size := o.Size
	if o.Blocks > 0 {
		size = o.Blocks * blockSize(o.Size)
	}
	passes := 1.0
	if o.Write {
		passes = 8
	}
	if o.ShortTest {
		b.order = append(b.order, "smart-short")
		b.est["smart-short"] = 120
	}
	b.order = append(b.order, "badblocks")
	b.est["badblocks"] = float64(size) * passes / assumedSpeed
	if o.LongTest {
		b.order = append(b.order, "smart-long")
		b.est["smart-long"] = float64(o.Size) / assumedSpeed
	}
	return b
}

// Status returns a copy of the live state.
func (b *BurnIn) Status() BurnInStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status
}

// update records the progress of the current step (pct 0..100), with the
// remaining time of the step when known (else derived from its estimate).
func (b *BurnIn) update(step string, pct float64, stepLeft float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if stepLeft > 0 && pct > 0 && pct < 100 {
		b.est[step] = stepLeft / (1 - pct/100)
	}
	var total, done, left float64
	after := false
	for _, s := range b.order {
		e := b.est[s]
		total += e
		switch {
		case s == step:
			done += e * pct / 100
			left += e * (1 - pct/100)
			after = true
		case after:
			left += e
		default:
			done += e
		}
	}
	b.status.Step, b.status.StepPct = step, pct
	if total > 0 {
		b.status.Progress = done / total * 100
	}
	b.status.ETA = int64(left)
	if b.setProg != nil {
		b.setProg(b.status.Progress)
	}
}

// selfTest is the state of a SMART self-test read from smartctl -j -c -l selftest.
type selfTest struct {
	Running   bool
	Remaining int    // percent left
	Passed    *bool  // result of the last completed test (nil: unknown)
	Result    string // text of the last result
	ShortMin  int    // polling minutes advertised by the disk
	LongMin   int
}

func parseSelfTest(data []byte) selfTest {
	var j struct {
		Ata struct {
			SelfTest struct {
				Status struct {
					Value     int    `json:"value"`
					String    string `json:"string"`
					Remaining int    `json:"remaining_percent"`
					Passed    *bool  `json:"passed"`
				} `json:"status"`
				Polling struct {
					Short    int `json:"short"`
					Extended int `json:"extended"`
				} `json:"polling_minutes"`
			} `json:"self_test"`
		} `json:"ata_smart_data"`
		AtaLog struct {
			Standard struct {
				Table []struct {
					Status struct {
						String string `json:"string"`
						Passed *bool  `json:"passed"`
					} `json:"status"`
				} `json:"table"`
			} `json:"standard"`
		} `json:"ata_smart_self_test_log"`
		NVMe *struct {
			Current struct {
				Value int `json:"value"`
			} `json:"current_self_test_operation"`
			Completion int `json:"current_self_test_completion_percent"`
			Table      []struct {
				Result struct {
					Value  int    `json:"value"`
					String string `json:"string"`
				} `json:"self_test_result"`
			} `json:"table"`
		} `json:"nvme_self_test_log"`
	}
	var st selfTest
	if json.Unmarshal(data, &j) != nil {
		return st
	}
	if n := j.NVMe; n != nil {
		st.Running = n.Current.Value != 0
		st.Remaining = 100 - n.Completion
		if len(n.Table) > 0 {
			ok := n.Table[0].Result.Value == 0
			st.Passed, st.Result = &ok, n.Table[0].Result.String
		}
		return st
	}
	s := j.Ata.SelfTest
	st.ShortMin, st.LongMin = s.Polling.Short, s.Polling.Extended
	// Execution status 0xF_: a test is in progress, the low nibble is tens of percent left.
	st.Running = s.Status.Value>>4 == 0xF
	st.Remaining = s.Status.Remaining
	if st.Running && st.Remaining == 0 {
		st.Remaining = (s.Status.Value & 0xF) * 10
	}
	if t := j.AtaLog.Standard.Table; len(t) > 0 {
		st.Passed, st.Result = t[0].Status.Passed, t[0].Status.String
	} else if !st.Running && s.Status.Passed != nil {
		st.Passed, st.Result = s.Status.Passed, s.Status.String
	}
	return st
}

func readSelfTest(dev string) selfTest {
	out, _ := sys.RunOpts(context.Background(), sys.Opts{AllowCodes: []int{-2}, Timeout: time.Minute}, "smartctl", "-j", "-c", "-l", "selftest", dev)
	return parseSelfTest([]byte(out))
}

// smartTest runs a SMART self-test and waits for its end (aborting it on cancel).
func (b *BurnIn) smartTest(ctx context.Context, kind string) store.BurnInStep {
	name := "smart-" + kind
	step := store.BurnInStep{Name: name}
	b.update(name, 0, 0)
	before := readSelfTest(b.o.Device)
	expect := before.ShortMin
	if kind == "long" {
		expect = before.LongMin
	}
	if expect > 0 {
		b.mu.Lock()
		b.est[name] = float64(expect * 60)
		b.mu.Unlock()
	}
	if err := storage.SmartTest(b.o.Device, kind); err != nil {
		step.Status, step.Detail = "skipped", "the disk refused the self-test: "+err.Error()
		return step
	}
	b.logf("SMART %s self-test started (about %d min announced by the disk)", kind, expect)
	start := time.Now()
	limit := time.Duration(max(expect, 2)*3)*time.Minute + 30*time.Minute
	time.Sleep(3 * time.Second)
	for {
		st := readSelfTest(b.o.Device)
		if !st.Running {
			break
		}
		pct := float64(100 - st.Remaining)
		left := 0.0
		if pct > 0 {
			left = time.Since(start).Seconds() * (100 - pct) / pct
		}
		b.update(name, pct, left)
		if time.Since(start) > limit {
			_, _ = sys.RunOpts(context.Background(), sys.Opts{AllowCodes: []int{-2}}, "smartctl", "-X", b.o.Device)
			step.Status, step.Detail = "failed", "the self-test did not finish in time"
			return step
		}
		select {
		case <-ctx.Done():
			_, _ = sys.RunOpts(context.Background(), sys.Opts{AllowCodes: []int{-2}}, "smartctl", "-X", b.o.Device)
			step.Status = "cancelled"
			return step
		case <-time.After(15 * time.Second):
		}
	}
	b.update(name, 100, 0)
	st := readSelfTest(b.o.Device)
	switch {
	case st.Passed == nil:
		step.Status, step.Detail = "passed", "result not reported by the disk"
	case *st.Passed:
		step.Status, step.Detail = "passed", st.Result
	default:
		step.Status, step.Detail = "failed", st.Result
	}
	return step
}

// badblocks runs the surface test. Cancelling sends SIGINT, which badblocks
// handles by stopping cleanly (writing out what it found).
func (b *BurnIn) badblocks(ctx context.Context) (store.BurnInStep, *BadblocksProgress) {
	step := store.BurnInStep{Name: "badblocks"}
	bs := blockSize(b.o.Size)
	args := []string{"-b", strconv.FormatInt(bs, 10), "-c", "256", "-s", "-v"}
	patterns := 0
	if b.o.Write {
		args = append(args, "-w")
		patterns = 4
	}
	args = append(args, b.o.Device)
	if b.o.Blocks > 0 {
		args = append(args, strconv.FormatInt(b.o.Blocks-1, 10), "0")
	}
	p := NewBadblocksProgress(patterns)
	b.update("badblocks", 0, 0)
	cmd := exec.Command("badblocks", args...)
	cmd.Env = []string{"LC_ALL=C", "LANG=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		step.Status, step.Detail = "failed", err.Error()
		return step, p
	}
	var bad strings.Builder
	cmd.Stdout = &limitedWriter{w: &bad, n: 64 << 10}
	b.logf("badblocks %s", strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		step.Status, step.Detail = "failed", err.Error()
		return step, p
	}
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGINT)
			select {
			case <-stop:
			case <-time.After(30 * time.Second):
				_ = cmd.Process.Kill()
			}
		case <-stop:
		}
	}()
	start := time.Now()
	buf := make([]byte, 4096)
	last := time.Time{}
	for {
		n, rerr := stderr.Read(buf)
		if n > 0 {
			p.Feed(string(buf[:n]))
			if time.Since(last) > 2*time.Second {
				last = time.Now()
				pct := p.Overall()
				left := 0.0
				if pct > 0.5 {
					left = time.Since(start).Seconds() * (100 - pct) / pct
				}
				b.update("badblocks", pct, left)
				b.mu.Lock()
				b.status.Errors = p.Errors()
				b.mu.Unlock()
			}
		}
		if rerr != nil {
			break
		}
	}
	err = cmd.Wait()
	close(stop)
	if !p.Completed {
		// Bad blocks are printed on stdout, one per line.
		p.BadBlocks = int64(strings.Count(bad.String(), "\n"))
	}
	switch {
	case ctx.Err() != nil:
		step.Status = "cancelled"
	case err != nil && !p.Completed:
		step.Status, step.Detail = "failed", "badblocks: "+err.Error()
	case p.BadBlocks > 0 || p.Errors() > 0:
		step.Status = "failed"
		step.Detail = fmt.Sprintf("%d bad block(s), %d read / %d write / %d comparison error(s)", p.BadBlocks, p.ReadErrors, p.WriteErrors, p.CorruptErrors)
	default:
		step.Status, step.Detail = "passed", fmt.Sprintf("no bad block in %s", time.Since(start).Round(time.Second))
		b.update("badblocks", 100, 0)
	}
	return step, p
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.n -= k
		_, _ = l.w.Write(p[:k])
	}
	return len(p), nil
}

// watched SMART counters: any increase during the test fails it.
var burnInAttrs = map[int]string{5: "Reallocated sectors", 197: "Pending sectors", 198: "Offline uncorrectable", 187: "Reported uncorrectable", 10: "Spin retries", 199: "UDMA CRC errors"}

func smartCounters(info *storage.SmartInfo) map[int]int {
	out := map[int]int{}
	if info == nil {
		return out
	}
	for _, a := range info.Attributes {
		if _, ok := burnInAttrs[a.ID]; ok {
			out[a.ID] = rawCount(a.Raw)
		}
	}
	if m, ok := info.NVMe.(map[string]any); ok {
		if v, ok := m["media_errors"].(float64); ok {
			out[-1] = int(v)
		}
	}
	return out
}

func rawCount(raw string) int {
	n := 0
	for _, c := range strings.TrimSpace(raw) {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			break
		}
	}
	return n
}

// CompareSmart lists the watched counters and the problems found between two readings.
func CompareSmart(before, after *storage.SmartInfo) (attrs []store.BurnInAttr, problems, warnings []string) {
	b, a := smartCounters(before), smartCounters(after)
	for _, id := range []int{5, 197, 198, 187, 10, 199, -1} {
		av, ok := a[id]
		if !ok {
			continue
		}
		name := burnInAttrs[id]
		if id == -1 {
			name = "Media errors"
		}
		bv := b[id]
		attrs = append(attrs, store.BurnInAttr{ID: id, Name: name, Before: bv, After: av})
		switch {
		case id == 199 && av > bv:
			warnings = append(warnings, fmt.Sprintf("%s: %d → %d (check the cable)", name, bv, av))
		case av > bv:
			problems = append(problems, fmt.Sprintf("%s: %d → %d", name, bv, av))
		case (id == 197 || id == 198) && av > 0:
			problems = append(problems, fmt.Sprintf("%s: %d", name, av))
		}
	}
	if after != nil && after.Passed != nil && !*after.Passed {
		problems = append(problems, "the disk fails its SMART health check")
	}
	return
}

// ErrCancelled marks a test stopped by the user.
var ErrCancelled = errors.New("burn-in test cancelled")

// Run performs the test and returns its report (also when it fails).
func (b *BurnIn) Run(ctx context.Context, rep *store.BurnInReport) {
	rep.Started = time.Now()
	rep.Steps = []store.BurnInStep{}
	smartOK := sys.Has("smartctl")
	var before *storage.SmartInfo
	if smartOK {
		if info, err := storage.Smart(b.o.Device); err == nil && info.Available {
			before = info
		}
	}
	add := func(s store.BurnInStep) bool {
		rep.Steps = append(rep.Steps, s)
		b.logf("%s: %s %s", s.Name, s.Status, s.Detail)
		return s.Status != "cancelled"
	}
	cont := true
	if b.o.ShortTest {
		if before == nil {
			cont = add(store.BurnInStep{Name: "smart-short", Status: "skipped", Detail: "no SMART data on this device"})
		} else {
			cont = add(b.smartTest(ctx, "short"))
		}
	}
	if cont {
		s, p := b.badblocks(ctx)
		rep.BadBlocks = p.BadBlocks
		cont = add(s)
	}
	if cont && b.o.LongTest {
		if before == nil {
			cont = add(store.BurnInStep{Name: "smart-long", Status: "skipped", Detail: "no SMART data on this device"})
		} else {
			cont = add(b.smartTest(ctx, "long"))
		}
	}
	if before != nil {
		if after, err := storage.Smart(b.o.Device); err == nil {
			rep.Attrs, rep.Problems, rep.Warnings = CompareSmart(before, after)
		}
	}
	for _, s := range rep.Steps {
		if s.Status == "failed" {
			rep.Problems = append(rep.Problems, s.Name+": "+s.Detail)
		}
	}
	switch {
	case !cont || ctx.Err() != nil:
		rep.Result = "cancelled"
	case len(rep.Problems) > 0:
		rep.Result = "failed"
	default:
		rep.Result = "passed"
	}
	rep.Finished = time.Now()
}
