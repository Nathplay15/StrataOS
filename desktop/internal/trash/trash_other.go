//go:build !linux && !darwin && !windows

package trash

import "errors"

func systemTrash(string) error { return errors.New("no system trash") }
