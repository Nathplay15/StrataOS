//go:build !(darwin && !cgo)

package tray

import (
	"path/filepath"
	"runtime"
	"time"

	"fyne.io/systray"

	"strataos-sync/internal/app"
	"strataos-sync/internal/icon"
)

// Available tells whether this build has a tray icon.
const Available = true

func iconBytes(s icon.State) []byte {
	if runtime.GOOS == "windows" {
		return icon.ICO(s, 16, 32)
	}
	return icon.PNG(s, 64)
}

// Run shows the icon until Quit is chosen (or quit is closed); it must run
// on the main goroutine. settings opens the settings page.
func Run(a *app.App, settings func(), quit <-chan struct{}, onExit func()) {
	systray.Run(func() { ready(a, settings, quit) }, onExit)
}

const maxFolders = 12

func ready(a *app.App, settings func(), quit <-chan struct{}) {
	systray.SetIcon(iconBytes(icon.Idle))
	systray.SetTooltip("StrataOS Sync")
	status := systray.AddMenuItem("StrataOS Sync", "")
	status.Disable()
	systray.AddSeparator()
	open := systray.AddMenuItem(a.T("tray.openfolder"), "")
	folders := systray.AddMenuItem(a.T("tray.openfolders"), "")
	var sub []*systray.MenuItem
	for range maxFolders {
		m := folders.AddSubMenuItem("", "")
		m.Hide()
		sub = append(sub, m)
	}
	web := systray.AddMenuItem(a.T("tray.openweb"), "")
	pause := systray.AddMenuItem(a.T("tray.pause"), "")
	syncNow := systray.AddMenuItem(a.T("tray.syncnow"), "")
	set := systray.AddMenuItem(a.T("tray.settings"), "")
	systray.AddSeparator()
	quitItem := systray.AddMenuItem(a.T("tray.quit"), "")

	last := icon.State(-1)
	update := func() {
		st := a.Status()
		s, line := summary(a, st)
		if s != last {
			systray.SetIcon(iconBytes(s))
			last = s
		}
		status.SetTitle(line)
		systray.SetTooltip("StrataOS Sync: " + line)
		open.SetTitle(a.T("tray.openfolder"))
		folders.SetTitle(a.T("tray.openfolders"))
		web.SetTitle(a.T("tray.openweb"))
		syncNow.SetTitle(a.T("tray.syncnow"))
		set.SetTitle(a.T("tray.settings"))
		quitItem.SetTitle(a.T("tray.quit"))
		if st.Paused {
			pause.SetTitle(a.T("tray.resume"))
		} else {
			pause.SetTitle(a.T("tray.pause"))
		}
		pairs := a.Config().Pairs
		if len(pairs) == 1 {
			open.Show()
			folders.Hide()
		} else {
			open.Hide()
			if len(pairs) == 0 {
				folders.Hide()
			} else {
				folders.Show()
			}
		}
		for i, m := range sub {
			if i < len(pairs) {
				m.SetTitle(filepath.Base(pairs[i].Local) + "  (" + pairs[i].Local + ")")
				m.Show()
			} else {
				m.Hide()
			}
		}
		if a.Paired() {
			web.Enable()
			pause.Enable()
			syncNow.Enable()
		} else {
			web.Disable()
			pause.Disable()
			syncNow.Disable()
		}
	}
	update()
	changed := make(chan struct{}, 1)
	a.OnChange(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	for i, m := range sub {
		go func() {
			for range m.ClickedCh {
				if pairs := a.Config().Pairs; i < len(pairs) {
					_ = a.Open("folder", pairs[i].ID)
				}
			}
		}()
	}
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				update()
			case <-changed:
				update()
			case <-open.ClickedCh:
				_ = a.Open("folder", "")
			case <-web.ClickedCh:
				_ = a.Open("web", "")
			case <-pause.ClickedCh:
				_ = a.SetPaused(!a.Status().Paused)
				update()
			case <-syncNow.ClickedCh:
				a.SyncNow()
			case <-set.ClickedCh:
				settings()
			case <-quitItem.ClickedCh:
				systray.Quit()
				return
			case <-quit:
				systray.Quit()
				return
			}
		}
	}()
}
