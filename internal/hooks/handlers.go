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

// bytesPerLine is a deliberately low guess used only to size a partial read
// (one with a line limit) for the token estimate.
const bytesPerLine = 60

// readStat describes the file a Read call is about to open.
type readStat struct {
	size    int64
	mtime   int64
	sum     string
	partial int64 // bytes the read will return when it is limited to some lines; else size
}

// statRead returns what a Read of the file would see, or ok=false for anything
// the hook should not judge: not an absolute path, not a regular file, empty,
// a paged (PDF) read, or too large to hash quickly.
func statRead(in Input, hash bool) (readStat, bool) {
	p := in.ToolInput.FilePath
	if !filepath.IsAbs(p) || in.ToolInput.Pages != "" {
		return readStat{}, false
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > maxHashBytes {
		return readStat{}, false
	}
	rs := readStat{size: fi.Size(), mtime: fi.ModTime().UnixNano(), partial: fi.Size()}
	if l := int64(in.ToolInput.Limit); l > 0 && l*bytesPerLine < rs.partial {
		rs.partial = l * bytesPerLine
	}
	if hash {
		sum, ok := fileSum(p, fi.Size(), maxHashBytes)
		if !ok {
			return readStat{}, false
		}
		rs.sum = sum
	}
	return rs, true
}

// session loads the session's state and the state the named optimizer runs in.
func (h *handler) session(key, name string, in Input) (*sessionState, string) {
	st := loadSession(h.dir, key)
	st.seePrompt(in.Prompt())
	return st, h.snap.State(name, st.Pins[name], h.dir, h.getenv)
}

// preRead is pre_tool_repeat_read. It acts only when the session already read
// this exact file range successfully, and the file has the same modification
// time, size and content hash now. In "on" it denies the call once so the agent
// reuses what it has; the agent's retry of that same version always goes through.
// A changed or unreadable file is never blocked.
func (h *handler) preRead(in Input, key string) ([]byte, error) {
	name := config.OptimizerHookRepeatRead
	if in.ToolName != "Read" {
		return nil, nil
	}
	st, state := h.session(key, name, in)
	if state == stateOff {
		return nil, nil
	}
	p := in.ToolInput.FilePath
	rk := readKey(p, int(in.ToolInput.Offset), int(in.ToolInput.Limit))
	ent := st.Reads[rk]
	if ent == nil {
		return nil, nil
	}
	rs, ok := statRead(in, true)
	if !ok || rs.mtime != ent.MTime || rs.size != ent.Size || rs.sum != ent.Sum {
		st.do(op{T: "del", K: rk}) // changed (or gone): the next successful read re-records it
		return nil, st.save(h.dir, key)
	}
	tokens := estTokens(rs.partial)
	if state == stateShadow {
		return nil, h.emit(key, name, KindWouldBlock, rs.partial, tokens)
	}
	if ent.BlockedSum == rs.sum {
		// We denied this version and the agent asked again: let it through, and
		// count it, because it means the block did not help.
		cleared := *ent
		cleared.BlockedSum = ""
		st.do(op{T: "read", K: rk, E: &cleared})
		if err := st.save(h.dir, key); err != nil {
			return nil, err
		}
		return nil, h.emit(key, name, KindRereadAnyway, rs.partial, tokens)
	}
	blocked := *ent
	blocked.BlockedSum = rs.sum
	st.do(op{T: "read", K: rk, E: &blocked})
	if err := st.save(h.dir, key); err != nil {
		return nil, err // if the denial cannot be remembered, do not deny: it could loop
	}
	if err := h.emit(key, name, KindBlocked, rs.partial, tokens); err != nil {
		return nil, err
	}
	return denyJSON(fmt.Sprintf("Trove: this file is unchanged since you read it earlier in this session (prompt #%d, %s ago), so its contents are already in your context. Use that. If you really need it read again, repeat the same Read and it will go through.",
		ent.Prompt, ago(h.now(), ent.At))), nil
}

func ago(now time.Time, at int64) string {
	d := now.Sub(time.Unix(at, 0))
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("about %d min", int(d.Minutes()))
	}
	return fmt.Sprintf("about %.1f h", d.Hours())
}

// postRead is post_tool_read_record. PostToolUse only fires for a Read that
// succeeded, so a denied or failed read is never taken for one that happened.
func (h *handler) postRead(in Input, key string) error {
	name := config.OptimizerHookRepeatRead
	if in.ToolName != "Read" {
		return nil
	}
	st, state := h.session(key, name, in)
	if state == stateOff {
		return nil
	}
	rs, ok := statRead(in, true)
	if !ok {
		return nil
	}
	st.do(op{T: "read", K: readKey(in.ToolInput.FilePath, int(in.ToolInput.Offset), int(in.ToolInput.Limit)),
		E: &readEntry{Sum: rs.sum, MTime: rs.mtime, Size: rs.size, Prompt: st.PromptN, At: h.now().Unix()}})
	return st.save(h.dir, key)
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

// largeOutput is pre_tool_large_output: for a known-noisy shell command it adds a
// hint to use trove_run. It never denies anything.
func (h *handler) largeOutput(in Input, key string) ([]byte, error) {
	name := config.OptimizerHookLargeOutput
	if in.ToolName != "Bash" || !IsNoisy(in.ToolInput.Command) {
		return nil, nil
	}
	_, state := h.session(key, name, in)
	switch {
	case state == stateOff:
		return nil, nil
	case state == stateShadow:
		return nil, h.emit(key, name, KindWouldAdvise, 0, 0)
	case !h.snap.TroveRun:
		return nil, nil // trove_run is not registered with the agent, so there is nothing to suggest
	}
	if err := h.emit(key, name, KindAdvised, 0, 0); err != nil {
		return nil, err
	}
	return contextJSON("Trove: this command usually prints a lot. If the trove_run tool is available, run it through trove_run: the full output stays retrievable and only the part that matters enters your context."), nil
}

// sessionEnd is session_end_receipt: it moves the hook counters into the
// database, runs the hook guardrail, writes this session's receipt, refreshes
// the snapshot the other hooks read, and forgets the session's ledger.
func (h *handler) sessionEnd(in Input, key string) error {
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
func Maintain(ctx context.Context, db *store.DB, dir string, cfg config.Config, getenv func(string) string, now time.Time) error {
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
