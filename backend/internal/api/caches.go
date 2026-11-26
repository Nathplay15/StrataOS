package api

import (
	"sync"
	"time"

	"strataos/internal/events"
	"strataos/internal/mediaplay"
)

var cachesOnce sync.Once

// subscribeCaches drops the cached views of the system when what they
// describe changes (internal/events): the GPU detection and the player's
// encoder after a component or package change or a transcoding setting, the
// Debian releases after a package change, the SMART readings when disks
// change, the single sign-on identities when accounts change.
func (s *Server) subscribeCaches() {
	cachesOnce.Do(func() {
		regpu := func(events.Event) {
			gpuForget()
			mediaplay.Default.Configure(s.gpuChoice)
		}
		events.Subscribe(events.Components, regpu)
		events.Subscribe(events.Packages, regpu)
		events.Subscribe(events.Settings, func(e events.Event) {
			if e.Topic == "transcoding" {
				regpu(e)
			}
		})
		events.Subscribe(events.Packages, func(events.Event) {
			distCache.Lock()
			distCache.at = time.Time{}
			distCache.Unlock()
		})
		events.Subscribe(events.Disks, func(events.Event) {
			smartCache.Range(func(k, _ any) bool {
				smartCache.Delete(k)
				return true
			})
		})
		events.Subscribe(events.Accounts, func(events.Event) { ssoForget() })
	})
}
