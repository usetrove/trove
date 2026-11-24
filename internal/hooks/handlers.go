package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/autopilot"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/external"
	"github.com/usetrove/trove/internal/features"
	"github.com/usetrove/trove/internal/mcp/install"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/store"
	"github.com/usetrove/trove/internal/tasks"
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
// When hook_read_outline is on and shortened the file, it replaces what the
// model sees with updatedToolOutput, changing only tool_response.file.content.
// Every other optimizer's result on a Read is recorded, never sent.
func (h *handler) postRead(in Input, key string) ([]byte, error) {
	if in.ToolName != "Read" {
		return nil, nil
	}
	h.featRead(in, key)
	st := h.session(key, in)
	ol := st.Pins[config.OptimizerHookReadOutline]
	h.recover = h.snap.TroveRun && ol != "" && ol != stateOff
	sess := h.sess(key, st, in.ToolName)
	res, reps := h.engine().TransformToolOutput(context.Background(), toolCall(in), ToolOutputOf(in), sess)
	if err := failure(reps); err != nil {
		return nil, err
	}
	applied := false
	for _, r := range reps {
		if r.Name != config.OptimizerHookReadOutline {
			continue
		}
		if r.Decision != nil {
			if err := h.emit(key, r.Name, r.Decision.Kind, 0, 0); err != nil {
				return nil, err
			}
			continue
		}
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
	if !applied || len(res.Parts) != 1 {
		return nil, nil
	}
	ids := optimize.OutlineIDs(res.Parts[0].Text)
	updated, ok := withReadContent(in.ToolResponse, res.Parts[0].Text)
	if !ok || len(ids) != 1 {
		return nil, nil
	}
	// Remember the outline only once it is sent, so a later full read or fetch is
	// counted against an outline the agent actually saw.
	optimize.MarkOutlined(sess.Ledger, in.ToolInput.FilePath, ids[0], sess.Ledger.Prompts(), h.now().Unix())
	if err := sess.Ledger.Save(); err != nil {
		return nil, err
	}
	return updatedOutputJSON(updated), nil
}

// reset is session_reset, registered for SessionStart and PreCompact. A session's
// optimizer states are pinned when it starts, so changes to config.toml apply
// from the next session on. After a compaction or a resume the earlier reads are
// no longer in the agent's context, so the ledger is cleared: a block then would
// leave the agent without the file.
//
// In the "on" arm of a rule-set trial it returns the rule set's text as SessionStart additionalContext, at
// startup and again after a resume, /clear or compaction, so the rules stay in the session's context.
func (h *handler) reset(in Input, key string) ([]byte, error) {
	if in.Event == "PreCompact" || in.Event == "PostCompact" {
		h.feat(key, "X")
	}
	st := loadSession(h.dir, key)
	if len(st.Pins) == 0 {
		for _, n := range OptimizerNames {
			st.do(op{T: "pin", N: n, V: h.snap.Pin(n)})
		}
		if v := h.snap.pinRatio(in.Model); v != "" {
			st.do(op{T: "pin", N: ratioPin, V: v})
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
		// A rule-set trial the user started is split the same way, for Claude Code only: the other agents'
		// SessionStart replies are not translated to carry context.
		if h.ruleTrialAgent() {
			for _, n := range h.snap.RuleTrial {
				if measure.Arm(key, n) == config.StateOn {
					st.do(op{T: "pin", N: n, V: stateOn})
					_ = h.emit(key, n, KindArmOn, 0, 0)
				} else {
					st.do(op{T: "pin", N: n, V: stateOff})
					_ = h.emit(key, n, KindArmOff, 0, 0)
				}
			}
		}
		// An installed scout or cascade is split the same way, so it is judged by a session-level A/B, and so is
		// every third-party hook tool Trove hands calls to.
		for _, n := range append(append([]string{}, h.snap.TaskTrial...), h.snap.HandoffTrial...) {
			if measure.Arm(key, n) == config.StateOn {
				st.do(op{T: "pin", N: n, V: stateOn})
				_ = h.emit(key, n, KindArmOn, 0, 0)
			} else {
				st.do(op{T: "pin", N: n, V: stateOff})
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
	h.armColdResume(in, st)
	if in.Event == "PreCompact" || in.Event == "PostCompact" || (in.Event == "SessionStart" && in.Source != "" && in.Source != "startup") {
		st.do(op{T: "clear"})
	}
	if err := st.save(h.dir, key); err != nil {
		return nil, err
	}
	if in.Event != "SessionStart" || !h.ruleTrialAgent() {
		return nil, nil
	}
	return ruleTrialContext(st.Pins), nil
}

func (h *handler) ruleTrialAgent() bool { return h.agent == "" || h.agent == AgentClaude }

// ruleTrialContext is the SessionStart reply that adds the rules of every rule-set trial this session is in the
// "on" arm of, or nil. The pin decides, so a session keeps its arm when the trial ends mid-way (rule 5). A tool
// Trove does not ship, or any error, adds nothing (rule 4).
func ruleTrialContext(pins map[string]string) []byte {
	var names []string
	for n, v := range pins {
		if config.IsRuleTrial(n) && v == stateOn {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var texts []string
	for _, n := range names {
		if m, ok := external.BuiltinRules(strings.TrimPrefix(n, external.Prefix)); ok {
			texts = append(texts, m)
		}
	}
	if len(texts) == 0 {
		return nil
	}
	return sessionContextJSON(strings.Join(texts, "\n\n"))
}

// subagent is pre_tool_subagent. In the "off" arm of a scout trial it declines a call to the scout subagent, so
// the session is a true control. Everything else, an unpinned session, a rolled-back scout and any error go
// through (rule 4). Cascade is started by the user from a slash command, so its off arm is only recorded.
func (h *handler) subagent(in Input, key string) ([]byte, error) {
	if in.ToolName != "Agent" && in.ToolName != "Task" {
		return nil, nil
	}
	if out := h.subagentModelControl(in, key); out != nil {
		return out, nil
	}
	if in.ToolInput.SubagentType != tasks.ScoutName {
		return nil, nil
	}
	if loadSession(h.dir, key).Pins[tasks.FeatureScout] != stateOff {
		return nil, nil
	}
	for _, n := range h.snap.RolledBack {
		if n == tasks.FeatureScout {
			return nil, nil
		}
	}
	return denyJSON("Trove is comparing sessions with and without the scout helper, and this session is one of the ones without it. Please do this work directly instead."), nil
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
	dedup := st.Pins[config.OptimizerHookOutputDedup]
	h.recover = h.snap.TroveRun && ((in.ToolName == "Bash" && pinned != "" && pinned != stateOff) || (dedup != "" && dedup != stateOff))
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
	// Remember which outputs were trimmed or collapsed, so a later fetch-back is
	// attributed to the optimizer that shortened them and not to trove_run, whose
	// output is fetched the same way.
	sess := h.sess(key, st, in.ToolName)
	for _, t := range out {
		for _, id := range optimize.TrimmedIDs(t) {
			sess.Ledger.Put(optimize.TrimmedKey(id), optimize.ReadEntry{Prompt: sess.Ledger.Prompts(), At: h.now().Unix()})
		}
		for _, id := range optimize.DedupIDs(t) {
			sess.Ledger.Put(optimize.DedupKey(id), optimize.ReadEntry{Prompt: sess.Ledger.Prompts(), At: h.now().Unix()})
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
	// The same output can be both trimmed once and collapsed later; each optimizer
	// that shortened it is charged one fetch-back.
	owners := []struct{ key, optimizer string }{
		{optimize.TrimmedKey(a.ID), config.OptimizerOutputTrimmer},
		{optimize.DedupKey(a.ID), config.OptimizerHookOutputDedup},
	}
	var charged []string
	for _, o := range owners {
		ent, ok := sess.Ledger.Get(o.key)
		if !ok || ent.BlockedSum != "" {
			continue // not this optimizer's, or already counted
		}
		ent.BlockedSum = "counted"
		sess.Ledger.Put(o.key, ent)
		charged = append(charged, o.optimizer)
	}
	// Fetching an outlined file whole is an extra read for hook_read_outline,
	// counted once with any full re-read of the same file.
	outlineFetched := optimize.CountOutlineFetch(sess.Ledger, a.ID)
	if len(charged) == 0 && !outlineFetched {
		return nil
	}
	if err := sess.Ledger.Save(); err != nil {
		return err
	}
	for _, name := range charged {
		if err := h.emit(key, name, KindFetchedBack, 0, 0); err != nil {
			return err
		}
	}
	if outlineFetched {
		return h.emit(key, config.OptimizerHookReadOutline, KindExtraRead, 0, 0)
	}
	return nil
}

// runTool is trove_run as the agent names it (server "trove").
const runTool = "mcp__trove__trove_run"

// adoptedKey is the session ledger key that remembers trove_run was already counted for this session.
const adoptedKey = "a:trove_run"

// postRun is post_tool_run. The first time in a session the agent uses trove_run it counts one adopted event
// for hook_large_output. The guardrail compares the sessions where the hook gave its hint with the ones where
// the agent then used trove_run: a hint nobody follows costs its text and saves nothing.
func (h *handler) postRun(in Input, key string) error {
	if in.ToolName != runTool {
		return nil
	}
	st := h.session(key, in)
	sess := h.sess(key, st, in.ToolName)
	if _, seen := sess.Ledger.Get(adoptedKey); seen {
		return nil
	}
	sess.Ledger.Put(adoptedKey, optimize.ReadEntry{Prompt: sess.Ledger.Prompts(), At: h.now().Unix(), BlockedSum: "counted"})
	if err := sess.Ledger.Save(); err != nil {
		return err
	}
	return h.emit(key, config.OptimizerHookLargeOutput, KindAdopted, 0, 0)
}

// sessionEnd is session_end_receipt: it moves the hook counters into the
// database, runs the hook guardrail, writes this session's receipt, refreshes
// the snapshot the other hooks read, and forgets the session's ledger.
func (h *handler) sessionEnd(in Input, key string) error {
	defer func() { _ = os.Remove(sessionPath(h.dir, key)) }()
	defer func() { _ = os.Remove(featPath(h.dir, key)) }()
	h.coldSessionEnd(in, key) // before Maintain, so its count is ingested now
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
	// The status line's plan-usage samples. Best effort: a spool left behind is picked up next time.
	_, _ = db.IngestLimitSpool(ctx, dir)
	// Refit the plan-usage meters when a newer reading arrived, for the status line and Autopilot.
	// Best effort and cached: a stale or missing fit only means no percentages are shown.
	_ = limitfit.Refresh(ctx, db, dir, now)
	// Retention must not depend on the gateway running: subscription users never start it. Once a day
	// across all processes, best effort (an old row surviving a day is never worth failing a hook for).
	_, _ = db.PurgeIfDue(ctx, cfg.RetentionDays, now)
	if _, err := measure.EvaluateHooksWith(ctx, db, cfg.Measure, now, measure.HookGuardOptions{AdoptionObservable: AdoptionObservable(dir)}); err != nil {
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
	if res, aerr := autopilot.Advance(ctx, db, dir, cfg, autopilot.CurrentPolicy(dir), now); aerr == nil {
		cfg = cfg.WithAutopilot(res.File)
	}
	// Revert tuning that did not help before the snapshot is written, so the next
	// session already starts with the defaults. Best effort: tuning must never stop
	// the rest of the bookkeeping.
	_, _ = tuning.Review(ctx, db, cfg.Measure, now)
	snap := BuildSnapshot(cfg, rolled, install.Registered(dir), now)
	// Learn bytes per token from the agent's own history. Best effort: without it estimates keep the default.
	if ratios, err := measure.LearnBytesPerToken(ctx, db, now); err == nil && len(ratios) > 0 {
		snap.BytesPerToken = ratios
	}
	if cfg.Experiment.Enabled {
		if feats, _, err := installedTasks(ctx, db); err == nil {
			snap.TaskTrial = feats
		}
		snap.HandoffTrial = HandoffTools(external.ActiveStates(ctx, db.ExternalTools()))
		// A damaged autopilot.json starts no trial (rule 4).
		if f, err := config.LoadAutopilot(dir); err == nil {
			snap.RuleTrial = f.RuleTrials()
		}
	}
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
