package ondemand

import "golang.org/x/sys/windows"

// diskSpace returns the size and free space of the disk holding dir.
func diskSpace(dir string) (total, free int64) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, 0
	}
	var avail, tot, all uint64
	if windows.GetDiskFreeSpaceEx(p, &avail, &tot, &all) != nil {
		return 0, 0
	}
	return int64(tot), int64(avail)
}
