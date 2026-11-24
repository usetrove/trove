package cli

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/store"
)

// What happened to the plan-usage meter, as statusOutput.LimitSample reports it. Reasons only; never a
// path or an error text, so nothing about the user's machine reaches the output.
const (
	sampleSpooled   = "spooled"   // appended to the spool; it reaches limit_samples at session end or `trove report`
	sampleUnchanged = "unchanged" // same values as the session's previous sample: nothing written
	sampleNoDataDir = "skipped_no_data_dir"
	sampleSpoolFull = "skipped_spool_full"
	sampleError     = "skipped_error"
)

// limitSampleTimeout caps the database check, so a slow disk can never make the status line slow
// (PRINCIPLES.md rule 4). The spool itself is one small file append.
var limitSampleTimeout = 25 * time.Millisecond

// recordLimitSample records Claude Code's plan-usage meter when a value changed since the session's
// previous sample. It never writes to the database and never takes its lock: it checks the session's
// marker (and, without one, the database's latest row on the status line's read-only connection, which
// may be nil) and appends one line to the spool. Every failure is swallowed
// and reported only as a reason; the status line's text and exit code never depend on it.
func recordLimitSample(ro *store.DB, dir string, in claudeInput, now time.Time) (result string) {
	if in.SessionID == "" || !in.hasMeter() {
		return ""
	}
	defer func() {
		if recover() != nil {
			result = sampleError
		}
	}()
	s := store.LimitSample{
		TS: now, SessionKey: session.ClaudeCodeKey(in.SessionID),
		CacheWarm: in.CacheWarm, CacheTTLSeconds: in.CacheTTLSeconds, Model: in.Model, CCVersion: in.Version,
	}
	if w := in.FiveHour; w != nil {
		s.FiveHourPct, s.FiveHourResetsAt = &w.UsedPercent, resetMillis(w.ResetsAt)
	}
	if w := in.SevenDay; w != nil {
		s.SevenDayPct, s.SevenDayResetsAt = &w.UsedPercent, resetMillis(w.ResetsAt)
	}

	// The common case, nothing changed since the last run, is one stat.
	if store.LimitSampleMarked(dir, s) {
		return sampleUnchanged
	}
	if ro != nil {
		// No marker (a new session, or markers swept): the database's latest row may already hold these
		// values. An error here (an older schema without the table, a slow disk) only means there is
		// nothing to compare with; the sample is spooled and the ingest's own check drops a repeat.
		ctx, cancel := context.WithTimeout(context.Background(), limitSampleTimeout)
		last, found, _ := ro.LastLimitSample(ctx, s.SessionKey)
		cancel()
		if found && last.SameValues(s) {
			_ = store.MarkLimitSample(dir, s)
			return sampleUnchanged
		}
	}
	switch err := store.AppendLimitSpool(dir, s); {
	case err == nil:
		return sampleSpooled
	case os.IsNotExist(err):
		return sampleNoDataDir // the status line never creates Trove's data folder
	case errors.Is(err, store.ErrLimitSpoolFull):
		return sampleSpoolFull
	default:
		return sampleError
	}
}

func resetMillis(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	ms := t.UnixMilli()
	return &ms
}
