// Package platform wraps what differs between Windows, macOS and Linux:
// opening folders and links, choosing a folder, notifications, start at
// login and the name of the system.
package platform

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Executable returns the absolute path of the running program.
func Executable() string {
	p, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// run starts a helper program without waiting for it.
func start(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	hide(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// output runs a helper program and returns its output.
func output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	hide(cmd)
	b, err := cmd.Output()
	return string(b), err
}
