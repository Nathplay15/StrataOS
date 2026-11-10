package agent

import (
	"context"
	"sync"
	"time"

	"strataos-sync/internal/api"
)

var probeCertificate = api.ProbeCertificate

// Event is something the window shows (a notification) or reloads for.
type Event struct {
	Seq     int64  `json:"seq"`
	Kind    string `json:"kind"` // notify, changed, transfer
	Account string `json:"account,omitempty"`
	Title   string `json:"title,omitempty"`
	Body    string `json:"body,omitempty"`
}

// eventLog keeps the last events for the window's long poll.
type eventLog struct {
	mu   sync.Mutex
	seq  int64
	list []Event
	wake chan struct{}
}

const keptEvents = 200

func (l *eventLog) init() { l.wake = make(chan struct{}) }

func (l *eventLog) add(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Several "changed" in a row say no more than one.
	if e.Kind == "changed" && len(l.list) > 0 {
		last := l.list[len(l.list)-1]
		if last.Kind == "changed" && last.Account == e.Account {
			l.seq++
			l.list[len(l.list)-1].Seq = l.seq
			l.signal()
			return
		}
	}
	l.seq++
	e.Seq = l.seq
	l.list = append(l.list, e)
	if len(l.list) > keptEvents {
		l.list = append([]Event(nil), l.list[len(l.list)-keptEvents:]...)
	}
	l.signal()
}

func (l *eventLog) signal() {
	close(l.wake)
	l.wake = make(chan struct{})
}

func (l *eventLog) last() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// since returns the events after seq.
func (l *eventLog) since(after int64) (int64, []Event, chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []Event{}
	for _, e := range l.list {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return l.seq, out, l.wake
}

// wait returns the events after seq, waiting up to d for one.
func (l *eventLog) wait(ctx context.Context, after int64, d time.Duration) (int64, []Event) {
	seq, evs, wake := l.since(after)
	if len(evs) > 0 || d <= 0 {
		return seq, evs
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-wake:
	case <-t.C:
	case <-ctx.Done():
	}
	seq, evs, _ = l.since(after)
	return seq, evs
}
