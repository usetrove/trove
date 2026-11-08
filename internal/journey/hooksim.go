package journey

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
)

// The hook-level simulation is what a Claude Code subscription user gets: the
// plugin's hooks run the real tool-level optimizers on each tool call, and no
// request is rewritten. Trove never sees subscription traffic, so this model
// reports tokens only, never dollars.
//
// It runs the real engine on real files: each session writes its read fixtures to
// a temporary directory, so repeat_read checks them against disk as it does in
// use. The agent's behavior is not modeled: a denied read is not retried.

// memLedger is the per-session read memory the repeat-read hook needs.
type memLedger struct {
	m map[string]optimize.ReadEntry
}

func newMemLedger() *memLedger { return &memLedger{m: map[string]optimize.ReadEntry{}} }

func (l *memLedger) Get(k string) (optimize.ReadEntry, bool) { e, ok := l.m[k]; return e, ok }
func (l *memLedger) Put(k string, e optimize.ReadEntry)      { l.m[k] = e }
func (l *memLedger) Delete(k string)                         { delete(l.m, k) }

// Prompts: each simulated session is one prompt. The hook's wording uses it.
func (l *memLedger) Prompts() int { return 1 }

// Save: nothing is persisted in the simulation.
func (l *memLedger) Save() error { return nil }

// runHookArm runs one conversation (the parent or a subagent) with the hooks on.
// It returns the bodies' context tokens and what the hooks did.
func (d *sessionDriver) runHookArm(turns []Turn, sessionIndex int, dir string) (Arm, error) {
	arm := newArm()
	arm.Turns = len(turns)
	arm.Checks = Checks{Structure: true}
	store := newMemStore()

	// Write each distinct read fixture so the real repeat-read check sees it on disk.
	written := map[string]bool{}
	for _, t := range turns {
		if t.Kind != KindReadSmall && t.Kind != KindReadLarge {
			continue
		}
		rel, err := readPathOf(t)
		if err != nil {
			return arm, err
		}
		if written[rel] {
			continue
		}
		written[rel] = true
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return arm, err
		}
		if err := os.WriteFile(abs, []byte(t.Output), 0o644); err != nil {
			return arm, fmt.Errorf("could not write a fixture file: %w", err)
		}
	}

	eng := optimize.New(optimize.Options{
		Config:   d.cfg,
		Getenv:   func(string) string { return "" },
		Now:      func() time.Time { return PricingDate },
		Recovery: store,
	})
	led := newMemLedger()
	state := func(string) string { return config.StateOn }
	ctx := context.Background()

	out := make([]Turn, len(turns))
	copy(out, turns)
	for i, t := range turns {
		s := optimize.Session{Key: fmt.Sprintf("hook-s%d", sessionIndex), Agent: "claude_code", TroveRun: true, State: state, Ledger: led}
		call := optimize.ToolCall{Name: t.ToolName, Input: json.RawMessage(t.ToolInput)}
		if t.Kind == KindReadSmall || t.Kind == KindReadLarge {
			rel, err := readPathOf(t)
			if err != nil {
				return arm, err
			}
			call.Input = json.RawMessage(fmt.Sprintf(`{"file_path":%s}`, jstr(filepath.Join(dir, rel))))
		}
		rewritten, reps := eng.TransformToolInput(ctx, call, s)
		denied := false
		for _, r := range reps {
			if r.Decision != nil && r.Decision.IsDeny() {
				denied = true
				out[i].Output = r.Decision.Message
				arm.Denied++
			}
			if r.Changed {
				arm.Applied[r.Name] += r.TokensRemoved
			}
		}
		if denied {
			continue
		}
		// A rewritten input would change what the tool runs, which this model does
		// not re-execute. It is counted, and the output is kept as the fixture.
		if string(rewritten.Input) != string(call.Input) {
			arm.Rewritten++
		}
		// The output hooks match Bash only (docs/hooks.md). A successful Read must
		// still reach the repeat-read ledger, so it is passed through the output
		// side, but its result is discarded: no output hook changes Read output.
		// Grep outputs are not transformed in this model.
		if t.ToolName == "Read" {
			_, _ = eng.TransformToolOutput(ctx, call, optimize.ToolOutput{Parts: []optimize.Part{{Text: t.Output}}}, s)
			continue
		}
		if t.ToolName != "Bash" {
			continue
		}
		res, reps2 := eng.TransformToolOutput(ctx, call, optimize.ToolOutput{Parts: []optimize.Part{{Text: t.Output}}}, s)
		for _, r := range reps2 {
			if r.Changed {
				arm.Applied[r.Name] += r.TokensRemoved
			}
		}
		if len(res.Parts) == 1 {
			out[i].Output = res.Parts[0].Text
		}
	}
	arm.Checks.Stored = store.count()
	arm.Checks.VerbatimRecovered = store.allOriginal(originalHashes(turns))

	// Context size: the agent holds these outputs in every later request, so each
	// request's body is what it processes. Summed over requests, that is the cost
	// a subscription user feels as usage, in tokens.
	bodies := d.bodies(out, simModels[0], true)
	for k, b := range bodies {
		if err := checkStructure(b, idsOf(turns[:k])); err != nil {
			arm.Checks.Structure = false
		}
		arm.Requests++
		arm.ContextTokens += tokens(len(b))
	}
	return arm, nil
}

// runHookOff is the same accounting with no hooks: the agent holds every output as
// the tool returned it.
func (d *sessionDriver) runHookOff(turns []Turn) Arm {
	arm := newArm()
	arm.Turns = len(turns)
	arm.Checks = Checks{Structure: true}
	bodies := d.bodies(turns, simModels[0], false)
	for k, b := range bodies {
		if err := checkStructure(b, idsOf(turns[:k])); err != nil {
			arm.Checks.Structure = false
		}
		arm.Requests++
		arm.ContextTokens += tokens(len(b))
	}
	return arm
}

// readPathOf extracts the relative fixture path from a read turn's input.
func readPathOf(t Turn) (string, error) {
	var in struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal([]byte(t.ToolInput), &in); err != nil || in.FilePath == "" {
		return "", fmt.Errorf("read turn %s has no file_path", t.ToolUseID)
	}
	return in.FilePath, nil
}

func idsOf(ts []Turn) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.ToolUseID)
	}
	return out
}
