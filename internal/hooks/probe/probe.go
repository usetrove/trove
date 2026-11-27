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
	"runtime"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/hooks"
)

// StateEnv names the environment variable that tells a probe hook where its
// state directory is. The agent passes its environment on to hook commands.
const StateEnv = "TROVE_PROBE_STATE"

// DefaultTools are the built-in tools probed by default. WebFetch needs the
// network and its own permission, so it is probed only when asked for.
var DefaultTools = defaultTools()

// defaultTools are the built-in tools, PowerShell where Claude Code has it on by default (Windows with Git Bash,
// code.claude.com/docs/en/tools-reference#powershell-tool, read 2026-11-25), and the MCP case. The large-Bash case
// is evidence only and runs when asked for (--tools BashLarge).
func defaultTools() []string {
	t := []string{"Bash", "Read", "Grep", ToolGrepContent, "Glob"}
	if runtime.GOOS == "windows" {
		t = append(t, "PowerShell")
	}
	return append(t, ToolMCP)
}

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
	// TroveBin is the trove binary, which serves the MCP server for the "mcp" case. Without it that case fails.
	TroveBin string
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
	rep.Capabilities.Entries = append(rep.Capabilities.Entries, CodexDocs(ctx, o, now), CursorDocs(now), GeminiDocs(ctx, o, now),
		ClaudePromptDocs(ver, now), ClaudeCodeWriteDocs(ver, "Write", now), ClaudeCodeWriteDocs(ver, "Edit", now))
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
	// AgentTool is the tool name the agent uses, when it differs from the probe's name for the case: "mcp" probes
	// Trove's own MCP tool mcpProbeTool, "BashLarge" probes Bash with an output past Claude Code's inline limit, and
	// "GrepContent" probes Grep in output_mode "content".
	AgentTool string `json:"agent_tool,omitempty"`
}

// agentTool is the tool the agent calls for this plan.
func (p plan) agentTool() string {
	if p.AgentTool != "" {
		return p.AgentTool
	}
	return p.Tool
}

// Probe cases beyond the built-in tools.
const (
	// ToolMCP stands for every MCP tool: whether Claude Code honours updatedToolOutput for an MCP result is a
	// property of MCP results, so one probe with Trove's own read-only trove_read answers it for all.
	ToolMCP = "mcp"
	// ToolGrepContent is Grep with output_mode "content", whose result is text rather than the file list the
	// "Grep" case gets. Grep output rewrites are gated on it (hookcaps.ToolGrepContent).
	ToolGrepContent = "GrepContent"
	// ToolBashLarge is a Bash call whose output is past Claude Code's 30,000-character inline limit. It records what
	// a PostToolUse hook is given for such an output (the whole text, or the saved file's path and a preview) and
	// whether a replacement still reaches the model. Its entry is evidence only: no optimizer is gated on it.
	ToolBashLarge = "BashLarge"
	// mcpProbeTool is the MCP tool the "mcp" case calls: trove_read from Trove's own server.
	mcpProbeTool = "mcp__trove__trove_read"
	// bashLargeLines is how many 21-byte lines the large Bash case prints: 42,000 bytes, past the 30,000 limit.
	bashLargeLines = 2000
	// bashLargeLine is the line the large Bash case prints, newline included.
	bashLargeLine = "TROVE_PROBE_SRC_LINE\n"
)

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
	switch tool {
	case ToolMCP:
		if o.TroveBin == "" {
			return c, 0, errors.New("the MCP case needs the trove binary to serve trove_read")
		}
		p.AgentTool = mcpProbeTool
	case ToolBashLarge:
		p.AgentTool = "Bash"
	case ToolGrepContent:
		p.AgentTool = "Grep"
	}
	prompt, err := setup(project, p)
	if err != nil {
		return c, 0, err
	}
	pb, _ := json.Marshal(p)
	if err := os.WriteFile(filepath.Join(state, "plan.json"), pb, 0o600); err != nil {
		return c, 0, err
	}
	settings := map[string]any{"hooks": map[string]any{
		"PreToolUse":  []any{map[string]any{"matcher": p.agentTool(), "hooks": []any{map[string]any{"type": "command", "command": o.HookCommand("pre"), "timeout": 10}}}},
		"PostToolUse": []any{map[string]any{"matcher": p.agentTool(), "hooks": []any{map[string]any{"type": "command", "command": o.HookCommand("post"), "timeout": 10}}}},
	}}
	sb, _ := json.Marshal(settings)
	settingsPath := filepath.Join(root, "settings.json")
	if err := os.WriteFile(settingsPath, sb, 0o600); err != nil {
		return c, 0, err
	}
	env := append(append([]string{}, orEnv(o.Env)...), StateEnv+"="+state)
	builtins := p.agentTool()
	var extra []string
	if tool == ToolMCP {
		// No built-in tools, and only Trove's MCP server, with its own throwaway data directory
		// (code.claude.com/docs/en/cli-reference: --tools "", --strict-mcp-config, --mcp-config; read 2026-11-25).
		builtins = ""
		mc, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"trove": map[string]any{
			"command": o.TroveBin, "args": []string{"mcp"}, "env": map[string]string{"TROVE_DATA_DIR": filepath.Join(root, "trove-data")}}}})
		extra = []string{"--strict-mcp-config", "--mcp-config", string(mc)}
	}
	args := []string{"-p", prompt, "--settings", settingsPath, "--setting-sources", "local", "--tools", builtins,
		"--allowedTools", p.agentTool(), "--output-format", "json", "--max-budget-usd", fmt.Sprintf("%.2f", capUSD),
		"--model", o.Model, "--no-session-persistence"}
	args = append(args, extra...)
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
		c.Evidence = "the hooks never ran: the agent did not call " + p.agentTool() + " in this session"
		return c, spent, nil
	}
	judge(&c, p, reply, events)
	if tool == ToolBashLarge {
		c.Evidence += "; " + largeEvidence(events)
	}
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

// largeEvidence says what the PostToolUse hook was given for the large Bash case: how long stdout was, and so
// whether the hook runs before or after Claude Code saves a long output to a file.
func largeEvidence(ev []seen) string {
	for _, e := range ev {
		if e.Mode != "post" {
			continue
		}
		var r struct {
			Stdout string `json:"stdout"`
		}
		if json.Unmarshal(e.Response, &r) != nil {
			return "the post hook's tool_response had no stdout string"
		}
		full := bashLargeLines * len(bashLargeLine)
		what := "less than the whole output, so the hook sees what Claude Code keeps, not the full text"
		if len(r.Stdout) >= full-1 {
			what = "the whole output, so the hook runs before Claude Code saves it to a file"
		}
		return fmt.Sprintf("the PostToolUse hook was given %d characters of stdout for a %d-character output: %s", len(r.Stdout), full, what)
	}
	return "the post hook never ran"
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
