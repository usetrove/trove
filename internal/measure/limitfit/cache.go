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

// ScaleFile holds only the two API-price scales, for the status line: reading the full fit (with its
// bootstrap rounds) took about 5 ms on the development machine, this about 0.5 ms.
const ScaleFile = "limitfit-scale.json"

// scales is ScaleFile's content.
type scales struct {
	ComputedAt int64  `json:"computed_at"`
	FiveHour   *Range `json:"five_hour_pct_per_api_usd,omitempty"`
	Week       *Range `json:"seven_day_pct_per_api_usd,omitempty"`
}

// WriteCache saves r and its scales atomically with permissions 0600.
func WriteCache(dir string, r Result) error {
	if err := writeJSON(dir, CacheFile, r); err != nil {
		return err
	}
	return writeJSON(dir, ScaleFile, scales{ComputedAt: r.ComputedAt, FiveHour: r.FiveHour.PerUSD, Week: r.Week.PerUSD})
}

func writeJSON(dir, file string, v any) error {
	b, err := json.Marshal(v)
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
	return os.Rename(name, filepath.Join(dir, file))
}

// ReadScales returns a fit holding only the API-price scales, enough for PercentUSD, or nil.
func ReadScales(dir string) *Result {
	b, err := os.ReadFile(filepath.Join(dir, ScaleFile))
	if err != nil {
		return nil
	}
	var s scales
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	r := &Result{ComputedAt: s.ComputedAt, FiveHour: MeterFit{Meter: FiveHour, Tier: TierNone}, Week: MeterFit{Meter: SevenDay, Tier: TierNone}}
	if s.FiveHour != nil {
		r.FiveHour.Tier, r.FiveHour.PerUSD = TierPrices, s.FiveHour
	}
	if s.Week != nil {
		r.Week.Tier, r.Week.PerUSD = TierPrices, s.Week
	}
	return r
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
