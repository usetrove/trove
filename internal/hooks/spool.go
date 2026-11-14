package hooks

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/redact"
	"github.com/usetrove/trove/internal/store"
)

// Kinds of hook decision, as stored in hook_events.kind.
const (
	KindBlocked      = "blocked"
	KindWouldBlock   = "would_block"
	KindAdvised      = "advised"
	KindWouldAdvise  = "would_advise"
	KindRereadAnyway = "reread_anyway"
	KindRewritten    = "rewritten"
	KindWouldRewrite = "would_rewrite"
	KindReissued     = "reissued"
	// KindFetchedBack: the agent called trove_fetch_full for output a hook trimmed.
	KindFetchedBack = "fetched_back"
	// KindArmOn and KindArmOff are not decisions. They record which side of a trial a session was
	// assigned when it started, so a session that never ends is still counted. They go to
	// experiment_assignments, not hook_events.
	KindArmOn  = "arm_on"
	KindArmOff = "arm_off"
)

// event is one line of the spool. Counts, sizes and a hashed session key only.
type event struct {
	T      int64  `json:"t"` // unix ms
	Key    string `json:"s"` // session key (a hash)
	Hook   string `json:"h"`
	Kind   string `json:"k"`
	Bytes  int64  `json:"b"`
	Tokens int64  `json:"n"` // estimated
}

const (
	spoolName = "events.jsonl"
	// maxSpool stops a spool nobody ingests from growing without bound; past it
	// new events are dropped (a count lost, never the agent's call).
	maxSpool = 4 << 20
	maxLog   = 256 << 10
)

func hooksDir(dir string) string { return filepath.Join(dir, "hooks") }

// record appends an event to the spool. One short O_APPEND write per event keeps
// concurrent hooks from interleaving lines.
func record(dir string, e event) error {
	p := filepath.Join(hooksDir(dir), spoolName)
	if fi, err := os.Stat(p); err == nil && fi.Size() > maxSpool {
		return fmt.Errorf("the hook spool is over %d bytes; run `trove hooks sync` to ingest it", maxSpool)
	}
	if err := os.MkdirAll(hooksDir(dir), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // Trove's own data dir
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// IngestSpool copies the spool into hook_events and deletes it. It claims the
// file by renaming it first, so hooks that fire meanwhile start a fresh spool,
// and a spool left behind by a crash is picked up next time. It returns how many
// events were stored.
func IngestSpool(ctx context.Context, db *store.DB, dir string) (int, error) {
	hd := hooksDir(dir)
	live := filepath.Join(hd, spoolName)
	claimed := filepath.Join(hd, fmt.Sprintf("events.ing.%d", time.Now().UnixNano()))
	if err := os.Rename(live, claimed); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	files, _ := filepath.Glob(filepath.Join(hd, "events.ing.*"))
	total := 0
	for _, f := range files {
		n, err := ingestFile(ctx, db, f)
		total += n
		if err != nil {
			return total, err
		}
		_ = os.Remove(f)
	}
	return total, nil
}

func ingestFile(ctx context.Context, db *store.DB, path string) (int, error) {
	f, err := os.Open(path) //nolint:gosec // Trove's own data dir
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO hook_events(ts, session_key, hook, kind, bytes, tokens) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stmt.Close() }()
	arm, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO experiment_assignments (session_key, optimizer, arm, stratum, seq, assigned_at) VALUES (?,?,?,'',0,?)`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = arm.Close() }()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil || !validKind(e.Kind) || e.Hook == "" || e.Key == "" {
			continue // a torn or foreign line; skip it
		}
		if e.Kind == KindArmOn || e.Kind == KindArmOff {
			// The first record for a session and optimizer wins, so an arm never changes.
			if _, err := arm.ExecContext(ctx, e.Key, e.Hook, strings.TrimPrefix(e.Kind, "arm_"), e.T); err != nil {
				return 0, err
			}
			n++
			continue
		}
		if _, err := stmt.ExecContext(ctx, e.T, e.Key, e.Hook, e.Kind, e.Bytes, e.Tokens); err != nil {
			return 0, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

func validKind(k string) bool {
	switch k {
	case KindBlocked, KindWouldBlock, KindAdvised, KindWouldAdvise, KindRereadAnyway, KindRewritten, KindWouldRewrite, KindReissued, KindFetchedBack, KindArmOn, KindArmOff:
		return true
	}
	return false
}

// scrub drops file names from an error so the log never holds a path the agent
// was working on (CLAUDE.md rule 7); the operation and cause stay.
func scrub(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Op + ": " + pe.Err.Error()
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Op + ": " + le.Err.Error()
	}
	return err.Error()
}

// logf appends one redacted line to <data dir>/hooks.log (0600, capped). It is
// how a hook reports its own errors, because stdout and stderr belong to the
// agent. It never fails the caller.
func logf(dir, event, format string, args ...any) {
	p := filepath.Join(dir, "hooks.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > maxLog {
		_ = os.Rename(p, p+".1")
	}
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // Trove's own data dir
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	for i, a := range args {
		if err, ok := a.(error); ok {
			args[i] = scrub(err)
		}
	}
	msg := strings.ReplaceAll(fmt.Sprintf(format, args...), "\n", " ")
	_, _ = fmt.Fprintf(f, "%s %s: %s\n", time.Now().UTC().Format(time.RFC3339), event, redact.String(msg))
}
