package main

import (
	"os"
	"syscall"
)

// attachConsole: the app is built without a console window; the commands
// print to the console they were started from.
func attachConsole() {
	if len(os.Args) < 2 {
		return
	}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("AttachConsole")
	const attachParent = ^uintptr(0) // ATTACH_PARENT_PROCESS
	if r, _, _ := proc.Call(attachParent); r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = f
	}
}
