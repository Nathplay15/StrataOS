package main

import "golang.org/x/sys/unix"

// pinPath keeps a file or folder of the on-demand mount on this computer
// (or frees it) through its extended attribute, served by the mount.
func pinPath(p string, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return unix.Setxattr(p, "user.strataos.pinned", []byte(v), 0)
}
