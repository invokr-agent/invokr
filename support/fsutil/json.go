package fsutil

import (
	"encoding/json"
	"errors"
	"os"
)

// ReadJSON decodes the JSON file at path into T. The bool reports whether the
// file existed: a missing file is not an error and yields the zero T with
// false. A decode failure returns true, so callers can tell "unreadable" from
// "unparseable" and wrap each with their own message.
func ReadJSON[T any](path string) (T, bool, error) {
	var value T
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		var zero T
		return zero, true, err
	}
	return value, true, nil
}

// WriteJSON writes v to path as indented JSON with a trailing newline. The
// write is a plain os.WriteFile, not an atomic replace.
func WriteJSON(path string, v any, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), perm)
}

// Exists reports whether path exists. A stat failure other than "not exist" is
// returned as an error rather than reported as absence.
func Exists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
