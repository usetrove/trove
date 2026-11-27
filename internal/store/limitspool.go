package store

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The limit spool. The status line must stay fast and never take the database's write lock, and on
// some systems opening, writing and closing SQLite costs tens of milliseconds. So the status line only
// appends one short line per changed sample to statusline/limits.jsonl, and IngestLimitSpool (run at
// session end and by `trove report`) moves the lines into limit_samples. Same metadata as the table,
// nothing more (PRINCIPLES.md rule 7).

// LimitSpoolDir is the folder in the data directory that holds the spool.
const LimitSpoolDir = "statusline"

const (
	limitSpoolName = "limits.jsonl"
	limitClaimGlob = "limits.ing.*"
	// MaxLimitSpool stops a spool nobody ingests from growing without bound. A line is about 250 bytes,
	// so this is roughly 4,000 changes; past it new samples are dropped, never the status line.
	MaxLimitSpool = 1 << 20
)

// ErrLimitSpoolFull means the spool reached MaxLimitSpool and the sample was dropped.
var ErrLimitSpoolFull = errors.New("the status line spool is full; run `trove report` or `trove hooks sync` to ingest it")

// limitLine is one spool line. Short keys keep the file small.
type limitLine struct {
	T   int64    `json:"t"` // unix ms
	S   string   `json:"s"` // hashed session key
	FP  *float64 `json:"fp,omitempty"`
	FR  *int64   `json:"fr,omitempty"`
	SP  *float64 `json:"sp,omitempty"`
	SR  *int64   `json:"sr,omitempty"`
	W   *bool    `json:"w,omitempty"`
	TTL *int64   `json:"ttl,omitempty"`
	M   string   `json:"m,omitempty"`
	V   string   `json:"v,omitempty"`
}

func toLine(s LimitSample) limitLine {
	return limitLine{T: s.TS.UnixMilli(), S: s.SessionKey, FP: s.FiveHourPct, FR: s.FiveHourResetsAt, SP: s.SevenDayPct,
		SR: s.SevenDayResetsAt, W: s.CacheWarm, TTL: s.CacheTTLSeconds, M: s.Model, V: s.CCVersion}
}

// sample turns a line back into a sample, dropping any value that could not have come from the status
// line, so a damaged or edited spool can never put free text in the database.
func (l limitLine) sample() (LimitSample, bool) {
	if l.S == "" || len(l.S) > 64 || strings.ContainsAny(l.S, " /\\") {
		return LimitSample{}, false
	}
	s := LimitSample{TS: time.UnixMilli(l.T), SessionKey: l.S, FiveHourPct: pct(l.FP), FiveHourResetsAt: l.FR,
		SevenDayPct: pct(l.SP), SevenDayResetsAt: l.SR, CacheWarm: l.W}
	if l.TTL != nil && (*l.TTL == 300 || *l.TTL == 3600) {
		s.CacheTTLSeconds = l.TTL
	}
	if ValidModelID(l.M) {
		s.Model = l.M
	}
	if ValidVersion(l.V) {
		s.CCVersion = l.V
	}
	return s, true
}

func pct(p *float64) *float64 {
	if p == nil || *p < 0 || *p > 100 || *p != *p {
		return nil
	}
	return p
}

func limitSpoolDir(dataDir string) string { return filepath.Join(dataDir, LimitSpoolDir) }

// AppendLimitSpool appends s to the spool. It never creates the data directory itself, only the spool
// folder inside an existing one, and writes one short O_APPEND line so concurrent status lines never
// interleave.
func AppendLimitSpool(dataDir string, s LimitSample) error {
	if s.SessionKey == "" {
		return errors.New("a limit sample needs a session key")
	}
	if _, err := os.Stat(dataDir); err != nil {
		return err
	}
	dir := limitSpoolDir(dataDir)
	p := filepath.Join(dir, limitSpoolName)
	if fi, err := os.Stat(p); err == nil && fi.Size() >= MaxLimitSpool {
		return ErrLimitSpoolFull
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(toLine(s))
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
	if werr != nil {
		return werr
	}
	return MarkLimitSample(dataDir, s)
}

// Markers. The status line runs after every assistant message and must not read back what it just
// wrote: on some systems the first read of a freshly written file waits for a malware scan. So the
// session's latest values are kept as the *name* of an empty file, statusline/seen/<session key>.<hash
// of the values>, and "did anything change?" is one stat. The name holds a hash and the already hashed
// session key, nothing else.
const limitSeenDir = "seen"

// markerMaxAge is how long a marker outlives its last change before an ingest sweeps it.
const markerMaxAge = 7 * 24 * time.Hour

func limitMarker(dataDir string, s LimitSample) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%v|%v|%v|%v|%v|%v|%q|%q",
		deref(s.FiveHourPct), deref(s.FiveHourResetsAt), deref(s.SevenDayPct), deref(s.SevenDayResetsAt),
		deref(s.CacheWarm), deref(s.CacheTTLSeconds), s.Model, s.CCVersion)))
	return filepath.Join(limitSpoolDir(dataDir), limitSeenDir, s.SessionKey+"."+hex.EncodeToString(h[:8]))
}

func deref[T any](p *T) any {
	if p == nil {
		return "-"
	}
	return *p
}

// validMarkerKey keeps a session key usable as a file name; keys are hashes ("c-" and hex), so anything
// else is refused rather than escaped.
func validMarkerKey(k string) bool {
	if k == "" || len(k) > 64 {
		return false
	}
	for _, r := range k {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// LimitSampleMarked reports whether s has the same values as the session's latest spooled or confirmed
// sample. It is a single stat.
func LimitSampleMarked(dataDir string, s LimitSample) bool {
	if !validMarkerKey(s.SessionKey) {
		return false
	}
	_, err := os.Stat(limitMarker(dataDir, s))
	return err == nil
}

// MarkLimitSample records s as the session's latest values, replacing the session's previous marker.
func MarkLimitSample(dataDir string, s LimitSample) error {
	if !validMarkerKey(s.SessionKey) {
		return errors.New("a limit sample's session key must be a hash")
	}
	p := limitMarker(dataDir, s)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // Trove's own data dir
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(filepath.Dir(p), s.SessionKey+".*"))
	for _, o := range old {
		if o != p {
			_ = os.Remove(o)
		}
	}
	return nil
}

// sweepLimitMarkers removes markers of sessions that have not changed for markerMaxAge.
func sweepLimitMarkers(dataDir string, now time.Time) {
	entries, err := os.ReadDir(filepath.Join(limitSpoolDir(dataDir), limitSeenDir))
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > markerMaxAge {
			_ = os.Remove(filepath.Join(limitSpoolDir(dataDir), limitSeenDir, e.Name()))
		}
	}
}

// scanSpool calls fn for each line of path, reading at most MaxLimitSpool bytes. A missing file has no lines.
func scanSpool(path string, fn func([]byte)) error {
	f, err := os.Open(path) //nolint:gosec // Trove's own data dir
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(io.LimitReader(f, MaxLimitSpool+4096))
	for sc.Scan() {
		fn(sc.Bytes())
	}
	return sc.Err()
}

// IngestLimitSpool moves the spool into limit_samples and deletes it. It claims the live file by renaming
// it first, so status lines that run meanwhile start a fresh one, and a claimed file left by a crash is
// picked up next time. Each line goes through the same changed-only check as a direct write, so a sample
// the status line repeated while an ingest was running is stored once. It returns how many rows it wrote.
func (d *DB) IngestLimitSpool(ctx context.Context, dataDir string) (int, error) {
	dir := limitSpoolDir(dataDir)
	sweepLimitMarkers(dataDir, time.Now())
	live := filepath.Join(dir, limitSpoolName)
	claimed := filepath.Join(dir, fmt.Sprintf("limits.ing.%019d", time.Now().UnixNano()))
	if err := os.Rename(live, claimed); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	files, err := filepath.Glob(filepath.Join(dir, limitClaimGlob))
	if err != nil {
		return 0, err
	}
	sort.Strings(files)
	total := 0
	for _, f := range files {
		n, err := d.ingestLimitFile(ctx, f)
		total += n
		if err != nil {
			return total, err
		}
		_ = os.Remove(f)
	}
	return total, nil
}

func (d *DB) ingestLimitFile(ctx context.Context, path string) (int, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	n := 0
	var ierr error
	err = scanSpool(path, func(line []byte) {
		if ierr != nil {
			return
		}
		var l limitLine
		if json.Unmarshal(line, &l) != nil {
			return // a torn line; skip it
		}
		s, ok := l.sample()
		if !ok {
			return
		}
		// A line no newer than the session's latest row is already stored (two ingests raced for the
		// same claimed file); storing it again would put an old value after a newer one.
		var latest sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(ts) FROM limit_samples WHERE session_key = ?`, s.SessionKey).Scan(&latest); err != nil {
			ierr = err
			return
		}
		if latest.Valid && latest.Int64 >= l.T {
			return
		}
		wrote, err := insertLimitSampleIfChanged(ctx, tx, s)
		if err != nil {
			ierr = err
			return
		}
		if wrote {
			n++
		}
	})
	if err == nil {
		err = ierr
	}
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// limitTailBytes is how much of the spool's end LatestSpooledLimitSample reads. A line is about 250 bytes, so this
// holds the last few hundred changes, across every session.
const limitTailBytes = 64 << 10

// LatestSpooledLimitSample returns the newest spooled sample of one session, reading only the end of the spool and
// never the database, so a hook can call it on its hot path. found is false when the session has no line there (no
// status line, or an ingest moved its lines into limit_samples). A line is written only when a value changed, so
// the sample's time is when it last changed, not when the last response arrived.
func LatestSpooledLimitSample(dataDir, sessionKey string) (s LimitSample, found bool) {
	if !validMarkerKey(sessionKey) {
		return LimitSample{}, false
	}
	f, err := os.Open(filepath.Join(limitSpoolDir(dataDir), limitSpoolName)) //nolint:gosec // Trove's own data dir
	if err != nil {
		return LimitSample{}, false
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return LimitSample{}, false
	}
	start := fi.Size() - limitTailBytes
	if start < 0 {
		start = 0
	}
	buf := make([]byte, fi.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return LimitSample{}, false
	}
	lines := strings.Split(string(buf), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // the first line was cut by the read; it may be any session's
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.Contains(lines[i], sessionKey) {
			continue
		}
		var l limitLine
		if json.Unmarshal([]byte(lines[i]), &l) != nil || l.S != sessionKey {
			continue
		}
		if s, ok := l.sample(); ok {
			return s, true
		}
	}
	return LimitSample{}, false
}
