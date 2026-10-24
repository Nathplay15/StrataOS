// mkicons writes the app icon for the packages: icon.png (256), icon.ico,
// icon.icns, into the folder given.
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
		"icon.png":  icon.PNG(icon.Idle, 256),
		"icon.ico":  icon.ICO(icon.Idle),
		"icon.icns": icon.ICNS(icon.Idle),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}
