package limitfit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// CacheFile is the latest fit, in the data folder. The status line and Autopilot read it so they never
// fit or query the database themselves. Numbers only (PRINCIPLES.md rule 7).
const CacheFile = "limitfit.json"

// WriteCache saves r atomically with permissions 0600.
func WriteCache(dir string, r Result) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".limitfit-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // a no-op once renamed
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrInvalid) {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, CacheFile))
}

// ReadCache returns the saved fit, or nil when there is none or it cannot be read (fail open: a
// caller then shows no percentage).
func ReadCache(dir string) *Result {
	b, err := os.ReadFile(filepath.Join(dir, CacheFile))
	if err != nil {
		return nil
	}
	var r Result
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// Stale reports whether a saved fit should be recomputed: there is none, a newer meter reading
// arrived since, or it is older than a day (history read since may have cleaned more intervals).
func Stale(r *Result, newestSample int64, now time.Time) bool {
	return r == nil || newestSample > r.LastSample || now.UnixMilli()-r.ComputedAt > 24*3600_000
}
