//go:build !windows

package engine

import (
	"io/fs"
	"runtime"
	"syscall"
)

// defaultFold: macOS volumes ignore case unless formatted otherwise.
var defaultFold = runtime.GOOS == "darwin"

func statID(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

// fileID returns the file ID of a path (scans already give it here).
func fileID(string) uint64 { return 0 }
