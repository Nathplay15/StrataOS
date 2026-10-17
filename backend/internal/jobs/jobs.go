// Package jobs runs long operations (mkfs, image pulls, apt upgrades...) in the background.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
)

const maxLog = 64 * 1024

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
	log      strings.Builder
	cancel   context.CancelFunc
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
}

// Write appends to the job log (implements io.Writer).
func (j *Job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.log.Len() < maxLog {
		j.log.Write(p)
	}
	return len(p), nil
}

func (j *Job) Logf(format string, args ...any) {
	fmt.Fprintf(j, format+"\n", args...)
}

func (j *Job) SetProgress(p float64) {
	j.mu.Lock()
	j.Progress = p
	j.mu.Unlock()
}

func (j *Job) snapshot(withLog bool) Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := Snapshot{ID: j.ID, Type: j.Type, Title: j.Title, User: j.User, Status: j.Status, Progress: j.Progress, Error: j.Error, Started: j.Started, Finished: j.Finished}
	if withLog {
		s.Log = j.log.String()
	}
	return s
}

type Manager struct {
	mu   sync.Mutex
	jobs map[string]*Job
	// OnFinish is called after each job completes.
	OnFinish func(Snapshot)
}

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
	j := &Job{ID: newID(), Type: typ, Title: title, User: user, Status: Running, Progress: -1, Started: time.Now(), cancel: cancel}
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
		if err != nil {
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

func (m *Manager) Get(id string) (Snapshot, bool) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return Snapshot{}, false
	}
	return j.snapshot(true), true
}

func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if ok {
		j.cancel()
	}
	return ok
}
