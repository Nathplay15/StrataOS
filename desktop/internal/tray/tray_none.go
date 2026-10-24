//go:build darwin && !cgo

package tray

import "strataos-sync/internal/app"

// Available tells whether this build has a tray icon (macOS needs cgo).
const Available = false

// Run waits until quit (no tray in this build: the settings page is the interface).
func Run(a *app.App, settings func(), quit <-chan struct{}, onExit func()) {
	settings()
	<-quit
	onExit()
}
