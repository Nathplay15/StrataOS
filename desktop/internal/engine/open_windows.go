package engine

import (
	"os"
	"syscall"
)

// openShared opens a file for reading without preventing others from
// renaming or deleting it (os.Open does not share deletion on Windows).
func openShared(p string) (*os.File, error) {
	ptr, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(ptr, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: p, Err: err}
	}
	return os.NewFile(uintptr(h), p), nil
}
