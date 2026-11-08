// Package probe checks, on the agent version that is installed, which tool calls
// a hook can really rewrite. The docs say PreToolUse updatedInput and PostToolUse
// updatedToolOutput work for built-in tools, but a release has shipped where the
// agent silently ignored one of them for some tools, and a value in the wrong
// shape is ignored without any error. Trust is therefore earned per tool by
// running the agent and seeing whether the model got the rewritten value.
//
// A live probe runs one tiny `claude -p` session per tool in a throwaway
// project, with hooks that rewrite the input to one marker and the output to
// another. The input is judged by what the PostToolUse hook is told the tool ran
// with; the output by whether the model's reply contains the output marker. Rerun
// it on every new agent release: `trove hooks probe --live` or
// `go run ./scripts/hookprobe --live`.
//
// A live probe starts model requests on its own, so it runs only when the caller
// has evidence that the agent uses the user's own API key (Options.APIKeyAuth) and
// is refused otherwise. For a Claude.ai login there is the session check in
// session.go, where the user starts the session and Trove starts nothing. Neither
// reads, copies or stores credentials, and neither makes network calls itself.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/hooks"
)

// StateEnv names the environment variable that tells a probe hook where its
// state directory is. The agent passes its environment on to hook commands.
const StateEnv = "TROVE_PROBE_STATE"

// DefaultTools are the built-in tools probed by default. WebFetch needs the
// network and its own permission, so it is probed only when asked for.
var DefaultTools = []string{"Bash", "Read", "Grep", "Glob"}

// Executor runs a program. Tests replace it so no real agent is started.
type Executor interface {
	Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error)
}

type osExec struct{}

func (osExec) Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the user's own agent binary
	cmd.Dir = dir
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.Bytes(), err
}

// ErrLiveNeedsAPIKey is why a live probe was refused.
var ErrLiveNeedsAPIKey = errors.New("a live probe starts its own Claude Code sessions, so it runs only when Claude Code uses your own API key; " +
	"Trove never starts model requests on a Claude.ai login (Free, Pro, Max, Ultra, Team or Enterprise). " +
	"Nothing was run. Test hook rewriting inside a session you start yourself instead: `trove hooks verify`. " +
	"If you do use an API key, set ANTHROPIC_API_KEY in this shell (or add `plan = \"api_key\"` under [history] in config.toml) and try again")

// Options configure a probe run.
type Options struct {
	Claude string // agent binary; default "claude"
	Live   bool   // run real sessions; otherwise report only what the docs say
	// APIKeyAuth says the caller found evidence that the agent authenticates with an
	// API key. A live run starts extra model requests on its own, which Trove does
	// only on the user's own metered key and never on a Claude.ai login (Free, Pro,
	// Max, Ultra, Team or Enterprise). Without it a live run is refused.
	APIKeyAuth bool
	MaxUSD     float64  // total spend cap for a live run; required when Live
	Model      string   // model for the sessions; default "claude-haiku-5-5"
	Tools      []string // default DefaultTools
	// HookCommand returns the shell command the agent runs for a probe hook. mode
	// is "pre" or "post".
	HookCommand func(mode string) string
	// RecordDir, if set, receives redacted copies of the hook inputs the agent
	// really sent, under <RecordDir>/<agent version>/. Tests use them as fixtures.
	RecordDir string
	Exec      Executor
	Now       func() time.Time
	Env       []string // default os.Environ()
}

// Report is the outcome of a run.
type Report struct {
	Capabilities hooks.Capabilities `json:"capabilities"`
	AgentVersion string             `json:"agent_version"`
	Live         bool               `json:"live"`
	SpentUSD     float64            `json:"spent_usd"`
	Notes        []string           `json:"notes,omitempty"`
}

// Run probes the agent.
func Run(ctx context.Context, o Options) (Report, error) {
	if o.Claude == "" {
		o.Claude = "claude"
	}
	if o.Exec == nil {
		o.Exec = osExec{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Model == "" {
		o.Model = "claude-haiku-5-5"
	}
	if len(o.Tools) == 0 {
		o.Tools = DefaultTools
	}
	if o.Env == nil {
		o.Env = os.Environ()
	}
	if o.Live {
		if !o.APIKeyAuth {
			return Report{}, ErrLiveNeedsAPIKey
		}
		if o.MaxUSD <= 0 {
			return Report{}, errors.New("a live probe runs real agent sessions and costs money; pass a spending cap, for example --max-usd 0.25")
		}
		if o.HookCommand == nil {
			return Report{}, errors.New("internal error: the live probe needs a hook command")
		}
	}
	ver, err := Version(ctx, o)
	rep := Report{AgentVersion: ver, Live: o.Live}
	if err != nil {
		return rep, fmt.Errorf("cannot run %q to read its version: %v. Install the agent or pass its path with --claude", o.Claude, err)
	}
	now := o.Now().UTC().Format(time.RFC3339)
	rep.Capabilities.Entries = append(rep.Capabilities.Entries, CodexDocs(ctx, o, now))
	if !o.Live {
		for _, t := range o.Tools {
			rep.Capabilities.Entries = append(rep.Capabilities.Entries, hooks.Capability{
				Agent: "claude", AgentVersion: ver, Tool: t, InputRewrite: hooks.CapUntested, OutputRewrite: hooks.CapUntested,
				Method: "docs", ProbedAt: now,
				Evidence: "the hooks reference documents updatedInput and updatedToolOutput for built-in tools; nothing was run, so Trove treats this as unsupported until a live probe confirms it"})
		}
		rep.Notes = append(rep.Notes, "Nothing was run. Use --live with --max-usd to test the installed version for real.")
		return rep, nil
	}
	per, err := perToolCap(o.MaxUSD, len(o.Tools))
	if err != nil {
		return rep, err
	}
	for _, t := range o.Tools {
		c, spent, err := probeTool(ctx, o, ver, t, per, now)
		rep.SpentUSD += spent
		if err != nil {
			c = hooks.Capability{Agent: "claude", AgentVersion: ver, Tool: t, InputRewrite: hooks.CapUntested, OutputRewrite: hooks.CapUntested,
				Method: "live", ProbedAt: now, Evidence: "the session did not complete: " + err.Error()}
		}
		rep.Capabilities.Entries = append(rep.Capabilities.Entries, c)
		if rep.SpentUSD >= o.MaxUSD {
			rep.Notes = append(rep.Notes, "stopped early: the spending cap was reached")
			break
		}
	}
	return rep, nil
}

// Version returns the agent's version number, for example "2.1.318".
func Version(ctx context.Context, o Options) (string, error) {
	if o.Exec == nil {
		o.Exec = osExec{}
	}
	out, err := o.Exec.Run(ctx, "", orEnv(o.Env), orStr(o.Claude, "claude"), "--version")
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return "", errors.New("it printed no version")
	}
	return f[0], nil
}

func orEnv(e []string) []string {
	if e == nil {
		return os.Environ()
	}
	return e
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// plan is what the hooks read to know what to rewrite.
type plan struct {
	Tool      string `json:"tool"`
	Nonce     string `json:"nonce"`
	MarkerIn  string `json:"marker_in"`
	MarkerOut string `json:"marker_out"`
}

// seen is one hook invocation, as the hooks record it.
type seen struct {
	Mode      string          `json:"mode"` // pre or post
	Tool      string          `json:"tool"`
	Input     json.RawMessage `json:"input"`
	Response  json.RawMessage `json:"response,omitempty"`
	Replaced  bool            `json:"replaced"`        // the hook sent a replacement
	ShapeNote string          `json:"shape,omitempty"` // why no replacement was sent
	Raw       json.RawMessage `json:"raw"`             // the whole hook input
}

func probeTool(ctx context.Context, o Options, ver, tool string, capUSD float64, now string) (hooks.Capability, float64, error) {
	c := hooks.Capability{Agent: "claude", AgentVersion: ver, Tool: tool, Method: "live", ProbedAt: now,
		InputRewrite: hooks.CapUntested, OutputRewrite: hooks.CapUntested}
	root, err := os.MkdirTemp("", "trove-hookprobe-")
	if err != nil {
		return c, 0, err
	}
	defer func() { _ = os.RemoveAll(root) }()
	project, state := filepath.Join(root, "project"), filepath.Join(root, "state")
	for _, d := range []string{project, state} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return c, 0, err
		}
	}
	nonce := fmt.Sprintf("%x", o.Now().UnixNano()&0xffffff)
	p := plan{Tool: tool, Nonce: nonce, MarkerIn: "TROVE_PROBE_IN_" + nonce, MarkerOut: "TROVE_PROBE_OUT_" + nonce}
	prompt, err := setup(project, p)
	if err != nil {
		return c, 0, err
	}
	pb, _ := json.Marshal(p)
	if err := os.WriteFile(filepath.Join(state, "plan.json"), pb, 0o600); err != nil {
		return c, 0, err
	}
	settings := map[string]any{"hooks": map[string]any{
		"PreToolUse":  []any{map[string]any{"matcher": tool, "hooks": []any{map[string]any{"type": "command", "command": o.HookCommand("pre"), "timeout": 10}}}},
		"PostToolUse": []any{map[string]any{"matcher": tool, "hooks": []any{map[string]any{"type": "command", "command": o.HookCommand("post"), "timeout": 10}}}},
	}}
	sb, _ := json.Marshal(settings)
	settingsPath := filepath.Join(root, "settings.json")
	if err := os.WriteFile(settingsPath, sb, 0o600); err != nil {
		return c, 0, err
	}
	env := append(append([]string{}, orEnv(o.Env)...), StateEnv+"="+state)
	args := []string{"-p", prompt, "--settings", settingsPath, "--setting-sources", "local", "--tools", tool,
		"--allowedTools", tool, "--output-format", "json", "--max-budget-usd", fmt.Sprintf("%.2f", capUSD),
		"--model", o.Model, "--no-session-persistence"}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	out, runErr := o.Exec.Run(ctx, project, env, orStr(o.Claude, "claude"), args...)
	reply, spent, agentErr := parseReply(out)
	events := readSeen(state)
	if o.RecordDir != "" {
		record(o.RecordDir, ver, tool, events)
	}
	if len(events) == 0 {
		if agentErr != "" {
			return c, spent, errors.New("the agent reported: " + agentErr)
		}
		if runErr != nil {
			return c, spent, fmt.Errorf("%v: %s", runErr, firstLine(string(out)))
		}
		c.Evidence = "the hooks never ran: the agent did not call " + tool + " in this session"
		return c, spent, nil
	}
	judge(&c, p, reply, events)
	return c, spent, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// parseReply reads `claude -p --output-format json`: the model's reply and the
// cost. Unknown output yields an empty reply, which the judge reads as "no".
func parseReply(out []byte) (reply string, cost float64, agentErr string) {
	var r struct {
		Result  string  `json:"result"`
		Cost    float64 `json:"total_cost_usd"`
		IsError bool    `json:"is_error"`
	}
	if i := bytes.IndexByte(out, '{'); i >= 0 {
		_ = json.Unmarshal(out[i:], &r)
	}
	if r.IsError {
		agentErr = firstLine(r.Result)
	}
	return r.Result, r.Cost, agentErr
}

func readSeen(state string) []seen {
	b, err := os.ReadFile(filepath.Join(state, "seen.jsonl")) //nolint:gosec // our own temp dir
	if err != nil {
		return nil
	}
	var out []seen
	for _, l := range bytes.Split(b, []byte("\n")) {
		var s seen
		if len(l) > 0 && json.Unmarshal(l, &s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// judge turns what the hooks saw and what the model said into a verdict.
func judge(c *hooks.Capability, p plan, reply string, ev []seen) {
	var pre, post *seen
	for i := range ev {
		switch ev[i].Mode {
		case "pre":
			pre = &ev[i]
		case "post":
			post = &ev[i]
		}
	}
	var why []string
	switch {
	case pre == nil || !pre.Replaced:
		c.InputRewrite = hooks.CapUntested
		why = append(why, "the pre hook did not send a replacement input")
	case post == nil:
		c.InputRewrite = hooks.CapNo
		why = append(why, "the tool never completed after the input rewrite, so it was not accepted")
	case inputApplied(p, post.Input):
		c.InputRewrite = hooks.CapYes
		why = append(why, "the tool ran with the rewritten input (seen by the PostToolUse hook)")
	default:
		c.InputRewrite = hooks.CapNo
		why = append(why, "the tool ran with its original input; updatedInput was ignored")
	}
	switch {
	case post == nil:
		c.OutputRewrite = hooks.CapUntested
		why = append(why, "the post hook never ran")
	case !post.Replaced:
		c.OutputRewrite = hooks.CapUntested
		why = append(why, "the post hook could not build a replacement in this tool's response shape ("+post.ShapeNote+"); recorded for a better probe")
	case strings.Contains(reply, p.MarkerOut):
		c.OutputRewrite = hooks.CapYes
		why = append(why, "the model's reply contained the replacement output")
	default:
		c.OutputRewrite = hooks.CapNo
		why = append(why, "the model never saw the replacement output; updatedToolOutput was ignored")
	}
	c.Evidence = strings.Join(why, "; ")
}

func inputApplied(p plan, in json.RawMessage) bool {
	s := string(in)
	switch p.Tool {
	case "Read":
		var m struct {
			Limit json.Number `json:"limit"`
		}
		return json.Unmarshal(in, &m) == nil && m.Limit.String() == "1"
	default:
		return strings.Contains(s, p.MarkerIn)
	}
}

// perToolCap splits a live run's cap across the probed tools in whole cents, rounding each share down. Rounding
// each share to the nearest cent could add up to more than the cap: $0.10 over four tools would be 4 x $0.03.
// A cap too small to give every tool a cent is refused, not silently raised.
func perToolCap(maxUSD float64, tools int) (float64, error) {
	if tools < 1 {
		return 0, errors.New("no tools to probe")
	}
	cents := math.Floor(maxUSD/float64(tools)*100 + 1e-9)
	if cents < 1 {
		return 0, fmt.Errorf("a cap of $%.2f cannot give each of %d tools at least one cent; raise --max-usd", maxUSD, tools)
	}
	return cents / 100, nil
}
