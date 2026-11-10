// mkicons writes the app icons, drawn by internal/icon, into the folder
// given: icon.png (256), icon.ico and icon.icns for the packages, the sizes
// Tauri bundles (32x32.png, 128x128.png, 128x128@2x.png) and the tray icon
// in each sync state (tray-idle.png…).
package main

import (
	"log"
	"os"
	"path/filepath"

	"strataos-sync/internal/icon"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: mkicons DIR")
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"icon.png":         icon.PNG(icon.Idle, 256),
		"icon.ico":         icon.ICO(icon.Idle),
		"icon.icns":        icon.ICNS(icon.Idle),
		"32x32.png":        icon.PNG(icon.Idle, 32),
		"128x128.png":      icon.PNG(icon.Idle, 128),
		"128x128@2x.png":   icon.PNG(icon.Idle, 256),
		"tray-idle.png":    icon.PNG(icon.Idle, 64),
		"tray-syncing.png": icon.PNG(icon.Syncing, 64),
		"tray-paused.png":  icon.PNG(icon.Paused, 64),
		"tray-error.png":   icon.PNG(icon.Error, 64),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}
