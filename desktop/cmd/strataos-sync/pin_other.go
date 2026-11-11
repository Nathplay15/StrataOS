//go:build !linux

package main

import "errors"

// pinPath: Explorer and Finder have their own commands.
func pinPath(p string, on bool) error {
	return errors.New("use the file manager's menu (Always keep on this device / Free up space, or Download Now / Remove Download)")
}
