package hooks

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/features"
	"github.com/usetrove/trove/internal/store"
)

// Session features (internal/features) are collected next to the ledger. Each
// per-call hook appends one short numeric line to <data dir>/hooks/sessions/
// <key>.feat; nothing is read back until the session ends, so a call pays for one
// append. Lines hold hashes and numbers only (never a path, command or output):
//
//	R <path hash>             a file was read
//	E <path hash>             a file was edited
//	C <input hash> <0|1>      a tool call, and whether its output had an error
//	T <bytes> <dup pct> <f>   a tool result: size, duplicate lines in percent, flags (1 error, 2 stack trace)
//	X                         a compaction
//
// Every writer fails open: a feature that cannot be recorded is dropped and the
// hook carries on (CLAUDE.md rule 4).
const (
	maxFeatBytes  = 256 << 10 // stop recording past this; features are an aggregate, not a log
	repoFreshness = 7 * 24 * time.Hour
	repoScanLimit = 500 * time.Millisecond
	flagError     = 1
	flagStack     = 2
)

func featPath(dir, key string) string { return filepath.Join(sessionDir(dir), key+".feat") }

// feat appends one line. Errors are ignored on purpose.
func (h *handler) feat(key, line string) {
	defer func() { _ = recover() }() // features must never break a hook
	p := featPath(h.dir, key)
	if fi, err := os.Stat(p); err == nil && fi.Size() > maxFeatBytes {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // Trove's own data dir
	if err != nil {
		return
	}
	_, _ = f.WriteString(line + "\n")
	_ = f.Close()
}

func hex64(s string) string { return strconv.FormatUint(features.HashPath(s), 16) }

// featRead records a file read.
func (h *handler) featRead(in Input, key string) {
	if in.ToolInput.FilePath != "" {
		h.feat(key, "R "+hex64(in.ToolInput.FilePath))
	}
}

// postEdit is post_tool_edit_record. It only records the edit for the session
// features and never produces output.
func (h *handler) postEdit(in Input, key string) error {
	switch in.ToolName {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		if in.ToolInput.FilePath != "" {
			h.feat(key, "E "+hex64(in.ToolInput.FilePath))
		}
	}
	return nil
}

// featResults records the features of one tool call's output.
func (h *handler) featResults(in Input, key string, texts []string) {
	defer func() { _ = recover() }()
	var res features.Result
	for _, t := range texts {
		r := features.ToolResult(in.ToolInput.FilePath, []byte(t))
		res.SizeBytes += r.SizeBytes
		res.HasError = res.HasError || r.HasError
		res.HasStackTrace = res.HasStackTrace || r.HasStackTrace
		if r.DupLineRatio > res.DupLineRatio {
			res.DupLineRatio = r.DupLineRatio
		}
	}
	flags := 0
	if res.HasError {
		flags |= flagError
	}
	if res.HasStackTrace {
		flags |= flagStack
	}
	call := string(in.ToolInput.Raw)
	if call == "" {
		call = in.ToolInput.Command
	}
	failed := 0
	if res.HasError {
		failed = 1
	}
	h.feat(key, fmt.Sprintf("C %s %d", hex64(in.ToolName+"\x00"+call), failed))
	h.feat(key, fmt.Sprintf("T %d %d %d", res.SizeBytes, int(res.DupLineRatio*100+0.5), flags))
}

// loadFeatures replays a session's feature journal into an accumulator.
func loadFeatures(dir, key string) *features.Session {
	s := features.NewSession()
	f, err := os.Open(featPath(dir, key)) //nolint:gosec // path under Trove's data dir
	if err != nil {
		return s
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Fields(sc.Text())
		if len(p) == 0 {
			continue
		}
		switch p[0] {
		case "R", "E":
			if len(p) == 2 {
				if v, err := strconv.ParseUint(p[1], 16, 64); err == nil {
					if p[0] == "R" {
						s.Read(v)
					} else {
						s.Edit(v)
					}
				}
			}
		case "C":
			if len(p) == 3 {
				if v, err := strconv.ParseUint(p[1], 16, 64); err == nil {
					s.Call(v, p[2] == "1")
				}
			}
		case "T":
			if len(p) == 4 {
				size, e1 := strconv.Atoi(p[1])
				dup, e2 := strconv.Atoi(p[2])
				fl, e3 := strconv.Atoi(p[3])
				if e1 == nil && e2 == nil && e3 == nil {
					s.Add(features.Result{SizeBytes: size, DupLineRatio: float64(dup) / 100,
						HasError: fl&flagError != 0, HasStackTrace: fl&flagStack != 0})
				}
			}
		case "X":
			s.Compaction()
		}
	}
	return s
}

// flushFeatures writes the session's features and, when stale, the repo's, to the
// database. It runs at session end after the receipt, so a failure here costs
// only the features.
func (h *handler) flushFeatures(ctx context.Context, db *store.DB, in Input, key string) error {
	defer func() { _ = os.Remove(featPath(h.dir, key)) }()
	snap := loadFeatures(h.dir, key).Snapshot()
	// Scout work shows up as subagent ("sidechain") requests in the history Trove
	// has read; it is the same session key the gateway and history parsers use.
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_requests WHERE session_key = ? AND sidechain = 1`, key).Scan(&snap.ScoutCalls)
	fs := db.Features()
	repoKey := ""
	if in.Cwd != "" {
		salt, err := fs.Salt(ctx)
		if err != nil {
			return err
		}
		repoKey = features.RepoKey(in.Cwd, salt)
		if r, ok, err := fs.Repo(ctx, repoKey); err == nil && (!ok || h.now().Sub(r.ComputedAt) > repoFreshness) {
			sctx, cancel := context.WithTimeout(ctx, repoScanLimit)
			repo := features.ScanRepo(sctx, in.Cwd)
			cancel()
			if err := fs.PutRepo(ctx, repoRow(repoKey, h.now(), repo)); err != nil {
				return err
			}
		}
	}
	return fs.PutSession(ctx, store.SessionFeaturesRow{
		SessionKey: key, RepoKey: repoKey, RecordedAt: h.now(),
		FilesRead: snap.FilesRead, FilesEdited: snap.FilesEdited, RepeatReads: snap.RepeatReads, ScoutCalls: snap.ScoutCalls,
		Compactions: snap.Compactions, Retries: snap.Retries, ToolResults: snap.ToolResults, LargeResults: snap.LargeResults,
		AvgDupRatio: snap.AvgDupRatio, ErrorShare: snap.ErrorShare, StackShare: snap.StackShare,
	})
}

func repoRow(key string, at time.Time, r features.Repo) store.RepoFeaturesRow {
	mix := make([]string, 0, len(r.LangMix))
	for _, l := range r.LangMix {
		mix = append(mix, fmt.Sprintf("%s:%d", l.Lang, l.Pct))
	}
	return store.RepoFeaturesRow{RepoKey: key, ComputedAt: at, SizeBucket: string(r.Size),
		LangMix: strings.Join(mix, ","), TestFramework: string(r.TestFramework)}
}
