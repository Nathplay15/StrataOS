// Package tray shows the icon of the app in the notification area (menu
// bar on macOS) with its menu: state, open a folder, open the NAS, pause,
// settings, quit.
package tray

import (
	"strataos-sync/internal/app"
	"strataos-sync/internal/engine"
	"strataos-sync/internal/icon"
)

// summary returns the state of the icon and the status line of the menu.
func summary(a *app.App, st engine.Status) (icon.State, string) {
	if !a.Paired() {
		return icon.Paused, a.T("summary.notpaired")
	}
	if st.SignedOut {
		return icon.Error, a.T("summary.signedout")
	}
	if st.Paused {
		return icon.Paused, a.T("state.paused")
	}
	busy, errs, brake := 0, false, false
	for _, p := range st.Pairs {
		switch p.State {
		case "syncing":
			busy += max(0, p.Pending-p.Done)
			if busy == 0 {
				busy = 1
			}
		case "error":
			errs = true
		case "brake":
			brake = true
		}
	}
	switch {
	case brake:
		return icon.Error, a.T("state.brake")
	case errs:
		return icon.Error, a.T("summary.error")
	case busy > 0:
		return icon.Syncing, a.T("summary.syncing", busy)
	}
	return icon.Idle, a.T("state.uptodate")
}
