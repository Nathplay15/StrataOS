//go:build !windows

package engine

import "os"

func openShared(p string) (*os.File, error) { return os.Open(p) }
