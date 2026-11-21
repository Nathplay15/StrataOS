// Package jobs runs long operations (mkfs, image pulls, apt upgrades...) in the background.
package jobs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	Running Status = "running"
	Success Status = "success"
	Failed  Status = "failed"
	// Cancelled: stopped by an administrator (not an error of the task).
	Cancelled Status = "cancelled"
)

const maxLog = 64 * 1024

// logfRoom: the room kept for the lines of Logf after maxLog.
const logfRoom = 32 * 1024

type Job struct {
	mu       sync.Mutex
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Title    string     `json:"title"`
	User     string     `json:"user"`
	Status   Status     `json:"status"`
	Progress float64    `json:"progress"` // 0..100, -1 = indeterminate
	Error    string     `json:"error,omitempty"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	// Cancellable says whether an administrator may stop the job now;
	// CancelReason explains why not (English, translated by the UI).
	Cancellable  bool   `json:"cancellable"`
	CancelReason string `json:"cancelReason,omitempty"`
	CancelledBy  string `json:"cancelledBy,omitempty"`
	log          strings.Builder
	cancel       context.CancelFunc
	// The lines written by Logf, as a key and arguments (logkeys.go): the
	// interface shows them in the reader's language.
	keys    []LogKey
	lines   int  // newlines written so far
	partial bool // the log does not end with a newline
}

// Snapshot is a lock-free copy suitable for JSON encoding.
type Snapshot struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Title    string     `json:"title"`
	User     string     `json:"user"`
	Status   Status     `json:"status"`
	Progress float64    `json:"progress"`
	Error    string     `json:"error,omitempty"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	Log      string     `json:"log,omitempty"`
	// LogKeys: the lines of Log written by Logf, to translate.
	LogKeys []LogKey `json:"logKeys,omitempty"`

	Cancellable  bool   `json:"cancellable"`
	CancelReason string `json:"cancelReason,omitempty"`
	CancelledBy  string `json:"cancelledBy,omitempty"`
}

// Write appends to the job log (implements io.Writer).
func (j *Job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.writeLocked(p)
	return len(p), nil
}

func (j *Job) writeLocked(p []byte) {
	j.writeCapped(p, maxLog)
}

// writeCapped appends p while the log is under limit. Command output stops
// at maxLog; the lines of Logf may go a little further (maxLog + logfRoom),
// so the last steps of a job stay visible after a long command output.
func (j *Job) writeCapped(p []byte, limit int) {
	if j.log.Len() >= limit || len(p) == 0 {
		return
	}
	j.log.Write(p)
	j.lines += bytes.Count(p, []byte{'\n'})
	j.partial = p[len(p)-1] != '\n'
}

// Logf adds a line to the log. The format and its arguments are also kept
// apart (a key with {0}, {1}… and the arguments as text) for the interface
// to show the line in the reader's language (frontend/src/i18n/server.ts).
func (j *Job) Logf(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	key, sargs, ok := MakeLogKey(format, args)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.log.Len() >= maxLog+logfRoom {
		return
	}
	if j.partial {
		j.writeCapped([]byte{'\n'}, maxLog+logfRoom)
	}
	line := j.lines
	j.writeCapped([]byte(text+"\n"), maxLog+logfRoom)
	if ok && len(j.keys) < maxLogKeys {
		j.keys = append(j.keys, LogKey{Line: line, Lines: strings.Count(text, "\n") + 1, Key: key, Args: sargs})
	}
}

func (j *Job) SetProgress(p float64) {
	j.mu.Lock()
	j.Progress = p
	j.mu.Unlock()
}

// SetCancellable changes whether the job may be stopped, for jobs whose
// phases differ (a copy can stop, a RAID rebuild cannot).
func (j *Job) SetCancellable(ok bool, reason string) {
	j.mu.Lock()
	j.Cancellable, j.CancelReason = ok, ""
	if !ok {
		j.CancelReason = reason
	}
	j.mu.Unlock()
}

// Cancelled reports whether an administrator stopped the job (and who).
func (j *Job) Cancelled() (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.CancelledBy, j.CancelledBy != ""
}

func (j *Job) snapshot(withLog bool) Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := Snapshot{ID: j.ID, Type: j.Type, Title: j.Title, User: j.User, Status: j.Status, Progress: j.Progress, Error: j.Error, Started: j.Started, Finished: j.Finished,
		Cancellable: j.Cancellable && j.Status == Running, CancelReason: j.CancelReason, CancelledBy: j.CancelledBy}
	if withLog {
		s.Log = j.log.String()
		s.LogKeys = slices.Clone(j.keys)
	}
	return s
}

type Manager struct {
	mu   sync.Mutex
	jobs map[string]*Job
	// OnFinish is called after each job completes.
	OnFinish func(Snapshot)
	// Policy says whether jobs of a type may be stopped by an administrator,
	// and why not (nil: every job can be stopped).
	Policy func(typ string) (bool, string)
}

// Errors of Cancel.
var (
	ErrNotFound = errors.New("job not found")
	ErrFinished = errors.New("this task has already finished")
)

// NotCancellable is returned by Cancel for a job that must not be stopped.
type NotCancellable struct{ Reason string }

func (e *NotCancellable) Error() string { return e.Reason }

func NewManager() *Manager {
	return &Manager{jobs: map[string]*Job{}}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Start launches fn in a goroutine and returns the job immediately.
func (m *Manager) Start(typ, title, user string, fn func(ctx context.Context, j *Job) error) Snapshot {
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{ID: newID(), Type: typ, Title: title, User: user, Status: Running, Progress: -1, Started: time.Now(), cancel: cancel, Cancellable: true}
	if m.Policy != nil {
		ok, reason := m.Policy(typ)
		j.SetCancellable(ok, reason)
	}
	m.mu.Lock()
	m.jobs[j.ID] = j
	m.gcLocked()
	m.mu.Unlock()
	go func() {
		defer cancel()
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			err = fn(ctx, j)
		}()
		now := time.Now()
		j.mu.Lock()
		j.Finished = &now
		if by := j.CancelledBy; by != "" && (err != nil || ctx.Err() != nil) {
			j.Status = Cancelled
			j.Error = ""
			if j.log.Len() < maxLog {
				j.log.WriteString("Stopped by " + by + "\n")
			}
		} else if err != nil {
			j.Status = Failed
			j.Error = err.Error()
		} else {
			j.Status = Success
			j.Progress = 100
		}
		j.mu.Unlock()
		if m.OnFinish != nil {
			m.OnFinish(j.snapshot(false))
		}
	}()
	return j.snapshot(false)
}

// gcLocked drops finished jobs older than a day.
func (m *Manager) gcLocked() {
	for id, j := range m.jobs {
		j.mu.Lock()
		old := j.Finished != nil && time.Since(*j.Finished) > 24*time.Hour
		j.mu.Unlock()
		if old {
			delete(m.jobs, id)
		}
	}
}

func (m *Manager) List() []Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Snapshot, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j.snapshot(false))
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Started.After(out[b].Started) })
	return out
}

// Running reports whether a job exists and is still running.
func (m *Manager) Running(id string) bool {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.Status == Running
}

func (m *Manager) Get(id string) (Snapshot, bool) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return Snapshot{}, false
	}
	return j.snapshot(true), true
}

// Cancel stops a running job on behalf of user: its context is cancelled
// (the commands it runs are terminated) and it ends with status Cancelled.
func (m *Manager) Cancel(id, user string) error {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	j.mu.Lock()
	switch {
	case j.Status != Running:
		j.mu.Unlock()
		return ErrFinished
	case !j.Cancellable:
		reason := j.CancelReason
		j.mu.Unlock()
		if reason == "" {
			reason = "this task cannot be stopped"
		}
		return &NotCancellable{Reason: reason}
	}
	if user == "" {
		user = "an administrator"
	}
	if j.CancelledBy == "" {
		j.CancelledBy = user
	}
	j.mu.Unlock()
	j.cancel()
	return nil
}

// Abort cancels a job whatever its policy, for StrataOS itself (a transfer
// or migration cancelled through its own flow). It reports whether the job
// exists; the job ends as failed unless it was also cancelled by a user.
func (m *Manager) Abort(id string) bool {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if ok {
		j.cancel()
	}
	return ok
}
