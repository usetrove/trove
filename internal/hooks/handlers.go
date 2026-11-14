package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/autopilot"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/external"
	"github.com/usetrove/trove/internal/features"
	"github.com/usetrove/trove/internal/mcp/install"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/tuning"
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
	return h.preTool(in, key)
}

// preTool runs the engine's tool-input optimizers on a call and renders the
// result: a deny, a note, or a rewritten input.
func (h *handler) preTool(in Input, key string) ([]byte, error) {
	st := h.session(key, in)
	cur, reps := h.engine().TransformToolInput(context.Background(), toolCall(in), h.sess(key, st, in.ToolName))
	return h.render(key, cur, reps)
}

// postRead is post_tool_read_record. PostToolUse only fires for a Read that
// succeeded, so a denied or failed read is never taken for one that happened.
func (h *handler) postRead(in Input, key string) error {
	if in.ToolName != "Read" {
		return nil
	}
	h.featRead(in, key)
	st := h.session(key, in)
	_, reps := h.engine().TransformToolOutput(context.Background(), toolCall(in), ToolOutputOf(in), h.sess(key, st, in.ToolName))
	return failure(reps)
}

// reset is session_reset, registered for SessionStart and PreCompact. A session's
// optimizer states are pinned when it starts, so changes to config.toml apply
// from the next session on. After a compaction or a resume the earlier reads are
// no longer in the agent's context, so the ledger is cleared: a block then would
// leave the agent without the file.
func (h *handler) reset(in Input, key string) error {
	if in.Event == "PreCompact" || in.Event == "PostCompact" {
		h.feat(key, "X")
	}
	st := loadSession(h.dir, key)
	if len(st.Pins) == 0 {
		for _, n := range OptimizerNames {
			st.do(op{T: "pin", N: n, V: h.snap.Pin(n)})
		}
		// An optimizer on trial is on for about half of sessions. The arm is a hash of the session key, so
		// it needs no database at the start of a session, and it is recorded in the spool so the comparison
		// counts the session even if it never ends.
		for _, n := range h.snap.Trial {
			arm := measure.Arm(key, n)
			if arm == config.StateOn {
				st.do(op{T: "pin", N: n, V: h.snap.lowered(n, stateOn)})
				_ = h.emit(key, n, KindArmOn, 0, 0)
			} else {
				_ = h.emit(key, n, KindArmOff, 0, 0)
			}
		}
		// Tuned values are pinned with the states, so they cannot change mid-session
		// (rule 5). The repo is the hash of the working directory; none is pinned
		// when the directory is unknown or the repo has no applied tuning.
		if in.Cwd != "" && h.snap.RepoSalt != "" {
			for k, v := range h.snap.Tuning[features.RepoKey(in.Cwd, h.snap.RepoSalt)] {
				st.do(op{T: "pin", N: tunePinPrefix + k, V: strconv.Itoa(v)})
			}
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
	return h.preTool(in, key)
}

// postOutput is post_tool_output. For Bash, Grep and Glob it runs the engine's
// tool-output optimizers and, if one in "on" state shortened the text, replaces
// what the model sees with updatedToolOutput in the tool's own response shape.
// It counts what each optimizer did (estimated, from sizes). Anything it does not
// understand, or any failure, lets the original output through.
func (h *handler) postOutput(in Input, key string) ([]byte, error) {
	switch in.ToolName {
	case "Bash", "Grep", "Glob":
	default:
		return nil, nil
	}
	keys, texts, ok := responseTexts(in.ToolResponse)
	if !ok || interrupted(in.ToolResponse) {
		return nil, nil
	}
	h.featResults(in, key, texts)
	st := h.session(key, in)
	// The trimmer needs the recovery store, and the store is only useful when the
	// agent can fetch the text back, so ask for it only then.
	pinned := st.Pins[config.OptimizerOutputTrimmer]
	h.recover = in.ToolName == "Bash" && h.snap.TroveRun && pinned != "" && pinned != stateOff
	res, reps := h.engine().TransformToolOutput(context.Background(), toolCall(in), outputForEngine(texts), h.sess(key, st, in.ToolName))
	if err := failure(reps); err != nil {
		return nil, err
	}
	applied := false
	for _, r := range reps {
		if !r.Changed {
			continue
		}
		kind := optimize.KindWouldRewrite
		if r.State == stateOn {
			kind, applied = optimize.KindRewritten, true
		}
		if err := h.emit(key, r.Name, kind, int64(r.BytesRemoved), r.TokensRemoved); err != nil {
			return nil, err
		}
	}
	if !applied || len(res.Parts) != len(texts) {
		return nil, nil
	}
	out := make([]string, len(res.Parts))
	for i, p := range res.Parts {
		out[i] = p.Text
	}
	updated, ok := withResponseTexts(in.ToolResponse, keys, out)
	if !ok {
		return nil, nil
	}
	// Remember which outputs were trimmed, so a later fetch-back is attributed to
	// the trimmer and not to trove_run, whose output is fetched the same way.
	sess := h.sess(key, st, in.ToolName)
	for _, t := range out {
		for _, id := range optimize.TrimmedIDs(t) {
			sess.Ledger.Put(optimize.TrimmedKey(id), optimize.ReadEntry{Prompt: sess.Ledger.Prompts(), At: h.now().Unix()})
		}
	}
	if err := sess.Ledger.Save(); err != nil {
		return nil, err
	}
	return updatedOutputJSON(updated), nil
}

// fetchTool is trove_fetch_full as the agent names it (server "trove").
const fetchTool = "mcp__trove__trove_fetch_full"

// postFetch is post_tool_fetch. When the agent fetches back an output a hook
// trimmed, it counts one fetched_back for the trimmer. The guardrail uses the
// share of trimmed outputs that come back: if the agent keeps needing them, the
// trimming is too aggressive and the trimmer goes back to shadow.
func (h *handler) postFetch(in Input, key string) error {
	if in.ToolName != fetchTool {
		return nil
	}
	var a struct {
		ID string `json:"output_id"`
	}
	if json.Unmarshal(in.ToolInput.Raw, &a) != nil || a.ID == "" {
		return nil
	}
	st := h.session(key, in)
	sess := h.sess(key, st, in.ToolName)
	ent, ok := sess.Ledger.Get(optimize.TrimmedKey(a.ID))
	if !ok || ent.BlockedSum != "" {
		return nil // not ours, or already counted
	}
	ent.BlockedSum = "counted"
	sess.Ledger.Put(optimize.TrimmedKey(a.ID), ent)
	if err := sess.Ledger.Save(); err != nil {
		return err
	}
	return h.emit(key, config.OptimizerOutputTrimmer, KindFetchedBack, 0, 0)
}

// sessionEnd is session_end_receipt: it moves the hook counters into the
// database, runs the hook guardrail, writes this session's receipt, refreshes
// the snapshot the other hooks read, and forgets the session's ledger.
func (h *handler) sessionEnd(in Input, key string) error {
	defer func() { _ = os.Remove(sessionPath(h.dir, key)) }()
	defer func() { _ = os.Remove(featPath(h.dir, key)) }()
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
	// Autopilot turn-on and rollback notices, once each. Best effort.
	_ = autopilot.Announce(h.dir, cfg.Notifications.Desktop, h.notifyText)
	// Last, and best effort: the receipt is already written.
	if err := h.flushFeatures(ctx, db, in, key); err != nil {
		return fmt.Errorf("cannot write the session features: %v", err)
	}
	return nil
}

// Maintain brings the hook bookkeeping up to date: it ingests the spool, applies
// the hook guardrail and rewrites the snapshot the hooks read. It is safe to run
// at any time and as often as wanted.
func Maintain(ctx context.Context, db *store.DB, dir string, cfg config.Config, getenv func(string) string, now time.Time) error {
	if _, err := IngestSpool(ctx, db, dir); err != nil {
		return fmt.Errorf("cannot ingest hook counts: %v", err)
	}
	// Retention must not depend on the gateway running: subscription users never start it. Once a day
	// across all processes, best effort (an old row surviving a day is never worth failing a hook for).
	_, _ = db.PurgeIfDue(ctx, cfg.RetentionDays, now)
	if _, err := measure.EvaluateHooks(ctx, db, cfg.Measure, now); err != nil {
		return fmt.Errorf("cannot check the hook guardrail: %v", err)
	}
	if _, err := external.GuardAll(ctx, db, dir, cfg.Measure, now); err != nil {
		return fmt.Errorf("cannot check third-party tools: %v", err)
	}
	rolled, err := measure.Overrides(ctx, db)
	if err != nil {
		return fmt.Errorf("cannot read guardrail rollbacks: %v", err)
	}
	// A trial of a hook technique is judged on the agent's own history, so read it first (only while one runs).
	scanTrialHistory(ctx, db, dir, cfg, getenv)
	// Autopilot: promote or roll back at this session boundary. Best effort, and a failure
	// leaves the saved states as they were, so it never turns anything on by accident.
	if res, aerr := autopilot.Advance(ctx, db, dir, cfg, autopilot.DefaultPolicy(), now); aerr == nil {
		cfg = cfg.WithAutopilot(res.File)
	}
	// Revert tuning that did not help before the snapshot is written, so the next
	// session already starts with the defaults. Best effort: tuning must never stop
	// the rest of the bookkeeping.
	_, _ = tuning.Review(ctx, db, cfg.Measure, now)
	snap := BuildSnapshot(cfg, rolled, install.Registered(dir), now)
	if applied, err := tuning.Applied(ctx, db); err == nil && len(applied) > 0 {
		if salt, err := db.Features().Salt(ctx); err == nil {
			snap.RepoSalt, snap.Tuning = salt, applied
		}
	}
	return WriteSnapshot(dir, snap)
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
