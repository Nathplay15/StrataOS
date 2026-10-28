package trash

import (
	"errors"
	"os"
	"path/filepath"
)

// systemTrash moves to ~/.Trash (the Finder's trash for the home disk).
func systemTrash(p string) error {
	h, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(h, ".Trash")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return errors.New("no trash")
	}
	return os.Rename(p, unique(dir, filepath.Base(p)))
}
