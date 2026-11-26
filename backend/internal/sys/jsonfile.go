package sys

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// LoadJSON reads the JSON file path into v. A missing file is not an error
// (v is left as it is). A file that does not decode is kept aside as
// path.corrupt-<time> (the next save would otherwise replace it, and what
// it held would be lost without a trace), logged, and reported.
func LoadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		aside := path + ".corrupt-" + time.Now().Format("20060102-150405")
		_ = os.WriteFile(aside, b, 0o600)
		slog.Error("state file unreadable, kept aside", "file", path, "copy", aside, "err", err)
		return fmt.Errorf("%s is damaged (a copy is kept as %s): %w", path, aside, err)
	}
	return nil
}
