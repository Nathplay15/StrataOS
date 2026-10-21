// Package storagetools holds the space analyzer, the duplicate finder and the
// disk burn-in test.
package storagetools

import (
	"runtime"
	"syscall"
)

// lowPriority pins the calling goroutine to its OS thread and gives that
// thread the idle I/O class and the lowest CPU priority, so that a scan never
// slows down file sharing. The caller must run in its own goroutine: the thread
// is thrown away when that goroutine ends (it stays locked), so the priority
// never leaks to other work of the daemon.
func lowPriority() {
	runtime.LockOSThread()
	tid := syscall.Gettid()
	const ioprioWhoProcess, ioprioClassIdle = 1, 3
	_, _, _ = syscall.Syscall(syscall.SYS_IOPRIO_SET, ioprioWhoProcess, uintptr(tid), uintptr(ioprioClassIdle<<13))
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, tid, 19)
}

// inBackground runs fn at low priority in a dedicated goroutine and waits for it.
func inBackground(fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		lowPriority()
		fn()
	}()
	<-done
}
