// Package events is a small in-process publish/subscribe bus for changes of
// the system that cached views depend on: a component installed or removed,
// packages changed, a setting saved, a disk added or removed, accounts
// changed. Caches subscribe and drop what they hold; the code that makes the
// change publishes it, without knowing the caches.
package events

import (
	"log/slog"
	"sync"
)

// Kind is what changed.
type Kind string

const (
	Components Kind = "components" // a component installed or removed
	Packages   Kind = "packages"   // Debian packages or APT sources changed
	Settings   Kind = "settings"   // a setting saved (the topic says which)
	Disks      Kind = "disks"      // a disk added, removed or replaced
	Accounts   Kind = "accounts"   // users or groups changed
)

// Event is one change; Topic narrows it ("transcoding" for Settings…).
type Event struct {
	Kind  Kind
	Topic string
}

var (
	mu   sync.RWMutex
	subs = map[Kind][]func(Event){}
)

// Subscribe calls fn for every event of kind. fn runs in the publisher's
// goroutine and must be quick (drop a cache, set a flag).
func Subscribe(kind Kind, fn func(Event)) {
	mu.Lock()
	defer mu.Unlock()
	subs[kind] = append(subs[kind], fn)
}

// Publish tells the subscribers of e.Kind; a subscriber that panics is
// logged and does not stop the others.
func Publish(kind Kind, topic string) {
	mu.RLock()
	fns := append([]func(Event){}, subs[kind]...)
	mu.RUnlock()
	e := Event{Kind: kind, Topic: topic}
	for _, fn := range fns {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("event subscriber failed", "kind", kind, "panic", r)
				}
			}()
			fn(e)
		}()
	}
}

// Reset drops every subscription (tests).
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	subs = map[Kind][]func(Event){}
}
