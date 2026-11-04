package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/mcp/install"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// session loads the session's state and counts the prompt.
func (h *handler) session(key string, in Input) *sessionState {
	st := loadSession(h.dir, key)
	st.seePrompt(in.Prompt())
	return st
}

// preRead is pre_tool_repeat_read. The decision is the engine's; this renders it
// and counts it.
func (h *handler) preRead(in Input, key string) ([]byte, error) {
	if in.ToolName != "Read" {
		return nil, nil
	}
	st, call := h.session(key, in), toolCall(in)
	_, reps := h.engine().TransformToolInput(context.Background(), call, h.sess(key, st))
	return h.render(key, reps)
}

// postRead is post_tool_read_record. PostToolUse only fires for a Read that
// succeeded, so a denied or failed read is never taken for one that happened.
func (h *handler) postRead(in Input, key string) error {
	if in.ToolName != "Read" {
		return nil
	}
	st := h.session(key, in)
	_, reps := h.engine().TransformToolOutput(context.Background(), toolCall(in), ToolOutputOf(in), h.sess(key, st))
	return failure(reps)
}

// reset is session_reset, registered for SessionStart and PreCompact. A session's
// optimizer states are pinned when it starts, so changes to config.toml apply
// from the next session on. After a compaction or a resume the earlier reads are
// no longer in the agent's context, so the ledger is cleared: a block then would
// leave the agent without the file.
func (h *handler) reset(in Input, key string) error {
	st := loadSession(h.dir, key)
	if len(st.Pins) == 0 {
		for _, n := range OptimizerNames {
			st.do(op{T: "pin", N: n, V: h.snap.Pin(n)})
		}
	}
	if in.Event == "PreCompact" || in.Event == "PostCompact" || (in.Event == "SessionStart" && in.Source != "" && in.Source != "startup") {
		st.do(op{T: "clear"})
	}
	return st.save(h.dir, key)
}

// largeOutput is pre_tool_large_output: for a known-noisy shell command the
// engine adds a hint to use trove_run. It never denies anything.
func (h *handler) largeOutput(in Input, key string) ([]byte, error) {
	if in.ToolName != "Bash" {
		return nil, nil
	}
	st := h.session(key, in)
	_, reps := h.engine().TransformToolInput(context.Background(), toolCall(in), h.sess(key, st))
	return h.render(key, reps)
}

// sessionEnd is session_end_receipt: it moves the hook counters into the
// database, runs the hook guardrail, writes this session's receipt, refreshes
// the snapshot the other hooks read, and forgets the session's ledger.
func (h *handler) sessionEnd(_ Input, key string) error {
	defer func() { _ = os.Remove(sessionPath(h.dir, key)) }()
	h.sweepOldSessions()
	ctx, cancel := context.WithTimeout(context.Background(), endDeadline-250*time.Millisecond)
	defer cancel()
	cfg, err := config.Load(h.dir, h.getenv)
	if err != nil {
		cfg = config.Default() // a bad config.toml must not stop the receipt
	}
	db, err := store.Open(h.dir)
	if err != nil {
		return fmt.Errorf("cannot open the database: %v. Run `trove doctor` for details", err)
	}
	defer func() { _ = db.Close() }()
	if err := Maintain(ctx, db, h.dir, cfg, h.getenv, h.now()); err != nil {
		return err
	}
	recs, err := measure.EndSession(ctx, db, cfg.Measure, key, h.now())
	if err != nil {
		return fmt.Errorf("cannot write the receipt: %v", err)
	}
	if cfg.Notifications.Desktop && h.notify != nil && len(recs) > 0 {
		h.notify(recs[len(recs)-1])
	}
	return nil
}

// Maintain brings the hook bookkeeping up to date: it ingests the spool, applies
// the hook guardrail and rewrites the snapshot the hooks read. It is safe to run
// at any time and as often as wanted.
func Maintain(ctx context.Context, db *store.DB, dir string, cfg config.Config, _ func(string) string, now time.Time) error {
	if _, err := IngestSpool(ctx, db, dir); err != nil {
		return fmt.Errorf("cannot ingest hook counts: %v", err)
	}
	if _, err := measure.EvaluateHooks(ctx, db, cfg.Measure, now); err != nil {
		return fmt.Errorf("cannot check the hook guardrail: %v", err)
	}
	rolled, err := measure.Overrides(ctx, db)
	if err != nil {
		return fmt.Errorf("cannot read guardrail rollbacks: %v", err)
	}
	return WriteSnapshot(dir, BuildSnapshot(cfg, rolled, install.Registered(dir), now))
}

// sweepOldSessions removes session files nobody has touched for a week (a
// session that never reached SessionEnd) and stray temp files.
func (h *handler) sweepOldSessions() {
	entries, err := os.ReadDir(sessionDir(h.dir))
	if err != nil {
		return
	}
	cutoff := h.now().Add(-7 * 24 * time.Hour)
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(sessionDir(h.dir), e.Name()))
		} else if strings.HasPrefix(e.Name(), ".tmp-") && err == nil && h.now().Sub(fi.ModTime()) > time.Minute {
			_ = os.Remove(filepath.Join(sessionDir(h.dir), e.Name()))
		}
	}
}
