//go:build windows

package engine

import (
	"io/fs"
	"syscall"
)

const defaultFold = true

// statID: a directory listing gives no file ID on Windows (fileID opens the file).
func statID(fs.FileInfo) uint64 { return 0 }

// fileID returns the NTFS file index of a path (0 when unknown), used to
// recognize moves.
func fileID(p string) uint64 {
	ptr, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return 0
	}
	h, err := syscall.CreateFile(ptr, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil,
		syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0
	}
	defer syscall.CloseHandle(h)
	var d syscall.ByHandleFileInformation
	if syscall.GetFileInformationByHandle(h, &d) != nil {
		return 0
	}
	return uint64(d.FileIndexHigh)<<32 | uint64(d.FileIndexLow)
}
