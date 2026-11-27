package probe

// The prompt-context check, for hook_routine_steer (docs/hooks.md, "Routine steer"). Claude Code's documentation says
// UserPromptSubmit additionalContext is "added to Claude's context alongside the submitted prompt" and saved in the
// transcript (code.claude.com/docs/en/hooks, read 2026-11-24), but not that it lands inside the new turn, after the
// cached prefix. If it went anywhere earlier (the system prompt, say), every steered prompt would rewrite the cache.
// So the steer only records what it would do until this check, in the user's own session, has seen:
//   - the test text land in the transcript after the previous turn's last request and before the new turn's first,
//   - the turn that carried it read the whole previous prompt from the cache, so adding it broke nothing, and
//   - the next turn read that whole prompt, test text included, from the cache, so it stays put afterwards.
//
// As with the other checks, Trove starts nothing: `trove hooks verify --prompt-context` writes a settings file with
// one test hook on SessionStart and UserPromptSubmit, and the user sends four short prompts in a session of their own.
// The hook adds the test text to the second prompt. On the fourth it reads the session's transcript in memory and
// records numbers only: line positions and token counts. It never records a prompt, a reply or the transcript's path.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/hooks"
)

// ContextPrompts are the four prompts the user sends, in order. Each asks for a one-word reply and no tools, so each
// turn is one request.
var ContextPrompts = [4]string{
	"Trove check 1 of 4. Reply with just OK and use no tools.",
	"Trove check 2 of 4. Reply with just OK and use no tools.",
	"Trove check 3 of 4. Reply with just OK and use no tools.",
	"Trove check 4 of 4. Reply with just OK and use no tools.",
}

// contextPromptKeys are the parts of ContextPrompts the analysis looks for in user lines.
var contextPromptKeys = [4]string{"Trove check 1 of 4.", "Trove check 2 of 4.", "Trove check 3 of 4.", "Trove check 4 of 4."}

// contextCacheSlack is how many tokens of a previous prompt a turn may fail to read from the cache and still pass. It
// is far below the test text's own size (contextPadding, about 250 tokens), so losing the text is always seen.
const contextCacheSlack = 50

// contextPadding makes the test text big enough that a turn which dropped it, or a prefix it broke, shows in the
// token counts.
var contextPadding = strings.Repeat("This line is test padding from Trove and needs no reply. ", 20)

type contextSession struct {
	Armed   string `json:"armed"`
	Expires string `json:"expires"`
	Nonce   string `json:"nonce"`
	Version string `json:"agent_version,omitempty"`
}

// ContextFindings is what the test hook recorded: counts, positions and token figures only.
type ContextFindings struct {
	Calls int `json:"calls"`
	// ModelSeen and ModelAllowed are about SessionStart's model field: whether it came, and whether it is one the
	// steer acts on (so the check also shows the steer will recognise the session).
	ModelSeen    bool `json:"model_seen"`
	ModelAllowed bool `json:"model_allowed"`
	// Analysed is true once the fourth prompt's hook read the transcript.
	Analysed bool `json:"analysed"`
	// Problem says what the analysis could not find, or "".
	Problem string `json:"problem,omitempty"`
	// MarkerPlaced: the test text's line is after turn 1's last request and before turn 2's first.
	MarkerPlaced bool `json:"marker_placed"`
	// Turn1Prompt is the whole prompt of turn 1's last request; Turn2Read what turn 2's first request read from the
	// cache. Turn2Prompt and Turn3Read are the same one turn later.
	Turn1Prompt int64 `json:"turn1_prompt_tokens"`
	Turn2Read   int64 `json:"turn2_cache_read_tokens"`
	Turn2Prompt int64 `json:"turn2_prompt_tokens"`
	Turn3Read   int64 `json:"turn3_cache_read_tokens"`
}

// ArmedContext is what ArmContext prepared.
type ArmedContext struct {
	State        string    `json:"state_dir"`
	SettingsPath string    `json:"settings_file"`
	Expires      string    `json:"expires"`
	Prompts      [4]string `json:"prompts"`
}

// ArmContextOptions configure ArmContext.
type ArmContextOptions struct {
	HookCommand func(state string) (command string, args []string)
	Version     string
	Now         func() time.Time
}

// ArmContext prepares the check in state. It runs nothing.
func ArmContext(state string, o ArmContextOptions) (ArmedContext, error) {
	if o.HookCommand == nil {
		return ArmedContext{}, errors.New("internal error: the context check needs a hook command")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if err := os.RemoveAll(state); err != nil {
		return ArmedContext{}, err
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		return ArmedContext{}, err
	}
	now := o.Now().UTC()
	s := contextSession{Armed: now.Format(time.RFC3339), Expires: now.Add(SessionTTL).Format(time.RFC3339),
		Nonce: fmt.Sprintf("%x", now.UnixNano()&0xffffff), Version: o.Version}
	sb, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(state, "context-session.json"), sb, 0o600); err != nil {
		return ArmedContext{}, err
	}
	cmd, args := o.HookCommand(state)
	h := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd, "args": args, "timeout": 10}}}}
	settings, _ := json.MarshalIndent(map[string]any{"hooks": map[string]any{"SessionStart": h, "UserPromptSubmit": h}}, "", "  ")
	a := ArmedContext{State: state, SettingsPath: filepath.Join(state, "settings.json"), Expires: s.Expires, Prompts: ContextPrompts}
	if err := os.WriteFile(a.SettingsPath, settings, 0o600); err != nil {
		return ArmedContext{}, err
	}
	return a, nil
}

// contextMarker is the test text the hook adds to the second prompt.
func contextMarker(nonce string) string { return "TROVE_PROBE_CTX_" + nonce }

// contextText is what the hook adds: the marker, then the padding.
func contextText(nonce string) string { return contextMarker(nonce) + " " + contextPadding }

// contextHookInput is the part of the hook input the test hook reads. The prompt is not a field: it is never read.
type contextHookInput struct {
	Event      string `json:"hook_event_name"`
	Model      string `json:"model"`
	Transcript string `json:"transcript_path"`
}

// ContextHookMain is the test hook. On SessionStart it records whether a model came and whether the steer would act
// on it. On UserPromptSubmit it counts the call, adds the test text on the second, and on the fourth reads the
// transcript and records the findings. It never blocks and never fails the prompt.
func ContextHookMain(stdin io.Reader, stdout io.Writer, getenv func(string) string) int {
	state := getenv(StateEnv)
	if state == "" {
		return 0
	}
	var in contextHookInput
	if json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&in) != nil {
		return 0
	}
	b, err := os.ReadFile(filepath.Join(state, "context-session.json")) //nolint:gosec // our own state dir
	var s contextSession
	if err != nil || json.Unmarshal(b, &s) != nil {
		return 0
	}
	if exp, err := time.Parse(time.RFC3339, s.Expires); err != nil || time.Now().After(exp) {
		return 0
	}
	f := readContextFindings(state)
	switch in.Event {
	case "SessionStart":
		f.ModelSeen = in.Model != ""
		f.ModelAllowed = hooks.SteerModelOK(in.Model)
		_ = writeContextFindings(state, f)
		return 0
	case "UserPromptSubmit":
	default:
		return 0
	}
	f.Calls++
	if f.Calls == 4 {
		analyseContext(&f, in.Transcript, contextMarker(s.Nonce))
	}
	if writeContextFindings(state, f) != nil {
		return 0 // unrecorded: add nothing, so a later reading cannot be confused
	}
	if f.Calls == 2 {
		out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "UserPromptSubmit", "additionalContext": contextText(s.Nonce)}})
		_, _ = stdout.Write(out)
	}
	return 0
}

func readContextFindings(state string) ContextFindings {
	var f ContextFindings
	b, err := os.ReadFile(filepath.Join(state, "context-findings.json")) //nolint:gosec // our own state dir
	if err == nil {
		_ = json.Unmarshal(b, &f)
	}
	return f
}

func writeContextFindings(state string, f ContextFindings) error {
	b, _ := json.Marshal(f)
	return os.WriteFile(filepath.Join(state, "context-findings.json"), b, 0o600)
}

// analyseContext reads the transcript in memory and fills in the findings.
func analyseContext(f *ContextFindings, path, marker string) {
	f.Analysed = true
	if path == "" {
		f.Problem = "the hook was not told where the session's transcript is"
		return
	}
	fh, err := os.Open(filepath.Clean(path))
	if err != nil {
		f.Problem = "the session's transcript could not be read"
		return
	}
	defer func() { _ = fh.Close() }()
	var lines [][]byte
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		lines = append(lines, append([]byte(nil), sc.Bytes()...))
	}
	analyseContextLines(f, lines, marker)
}

// ctxLine is the part of a transcript line the analysis reads. These fields are observed in transcripts, not
// documented (docs/logs.md).
type ctxLine struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Message     *struct {
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			Input         int64 `json:"input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// analyseContextLines is the pure part: given the transcript's lines, find the four prompts, the test text and the
// requests around them.
func analyseContextLines(f *ContextFindings, lines [][]byte, marker string) {
	promptAt := [4]int{-1, -1, -1, -1}
	markerAt := -1
	type req struct {
		at           int
		prompt, read int64
		hasUsage     bool
	}
	var reqs []req
	for i, ln := range lines {
		if markerAt < 0 && bytes.Contains(ln, []byte(marker)) {
			markerAt = i
			continue // the line carrying the test text is not a prompt or a request
		}
		var l ctxLine
		if json.Unmarshal(ln, &l) != nil || l.IsSidechain || l.Message == nil {
			continue
		}
		switch l.Type {
		case "user":
			for k := range promptAt {
				if promptAt[k] < 0 && bytes.Contains(l.Message.Content, []byte(contextPromptKeys[k])) && !bytes.Contains(l.Message.Content, []byte(`"tool_result"`)) {
					promptAt[k] = i
					break
				}
			}
		case "assistant":
			if u := l.Message.Usage; u != nil {
				reqs = append(reqs, req{at: i, prompt: u.Input + u.CacheRead + u.CacheCreation, read: u.CacheRead, hasUsage: true})
			}
		}
	}
	for k := 0; k < 3; k++ {
		if promptAt[k] < 0 {
			f.Problem = fmt.Sprintf("prompt %d of the check is not in the transcript; send the four prompts in order, in one session", k+1)
			return
		}
	}
	if markerAt < 0 {
		f.Problem = "the test text is not in the transcript, so where it went cannot be checked"
		return
	}
	// The last request of a turn and the first of the next. Each turn is one request when the model follows the
	// prompt, but a tool call would add more; the boundaries hold either way.
	lastBefore := func(at int) (req, bool) {
		var out req
		ok := false
		for _, r := range reqs {
			if r.at < at {
				out, ok = r, true
			}
		}
		return out, ok
	}
	firstAfter := func(at int) (req, bool) {
		for _, r := range reqs {
			if r.at > at {
				return r, true
			}
		}
		return req{}, false
	}
	t1Last, ok1 := lastBefore(promptAt[1])
	t2First, ok2 := firstAfter(promptAt[1])
	t2Last, ok3 := lastBefore(promptAt[2])
	t3First, ok4 := firstAfter(promptAt[2])
	// The fourth prompt may not be in the transcript yet: its hook runs before Claude Code handles it.
	if !ok1 || !ok2 || !ok3 || !ok4 || t2First.at > promptAt[2] || (promptAt[3] >= 0 && t3First.at > promptAt[3]) {
		f.Problem = "the transcript does not have a reply with token usage for each of the first three prompts"
		return
	}
	f.MarkerPlaced = markerAt > t1Last.at && markerAt < t2First.at
	f.Turn1Prompt, f.Turn2Read = t1Last.prompt, t2First.read
	f.Turn2Prompt, f.Turn3Read = t2Last.prompt, t3First.read
}

// FinishContextOptions configure FinishContext.
type FinishContextOptions struct {
	Version string
	Now     func() time.Time
	// Prior is the stored UserPromptSubmit entry, if any: its block result is kept as it is.
	Prior *hooks.Capability
}

// ContextResult is the check's outcome.
type ContextResult struct {
	Capability hooks.Capability `json:"capability"`
	Findings   ContextFindings  `json:"findings"`
}

// FinishContext reads what the test hook recorded and decides.
func FinishContext(state string, o FinishContextOptions) (ContextResult, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	b, err := os.ReadFile(filepath.Join(state, "context-session.json")) //nolint:gosec // our own state dir
	var s contextSession
	if err != nil || json.Unmarshal(b, &s) != nil {
		return ContextResult{}, errors.New("no context check is waiting. Run `trove hooks verify --prompt-context` first")
	}
	ver := o.Version
	if ver == "" {
		ver = s.Version
	}
	if ver == "" {
		ver = "unknown"
	}
	c := hooks.Capability{Agent: "claude", AgentVersion: ver, Tool: hookcaps.ToolPrompt, Method: "session",
		InputRewrite: hooks.CapNo, OutputRewrite: hooks.CapNo, Block: hooks.CapUntested}
	if p := o.Prior; p != nil && p.Agent == "claude" && p.Tool == hookcaps.ToolPrompt && (p.Method == "session" || p.Method == "live") {
		c = *p // keep the block result and its evidence
		c.AgentVersion = ver
	}
	c.ProbedAt = o.Now().UTC().Format(time.RFC3339)
	f := readContextFindings(state)
	c.Context, c.ContextEvidence = decideContext(f)
	return ContextResult{Capability: c, Findings: f}, nil
}

// decideContext is the pure verdict.
func decideContext(f ContextFindings) (result, evidence string) {
	switch {
	case f.Calls < 4:
		return hooks.CapUntested, fmt.Sprintf("the test hook ran for %d of the 4 prompts; send all four in one session", f.Calls)
	case !f.Analysed:
		return hooks.CapUntested, "the fourth prompt's hook did not read the transcript"
	case f.Problem != "":
		return hooks.CapUntested, f.Problem
	case !f.MarkerPlaced:
		return hooks.CapNo, "the test text was not between the previous turn's last request and the new turn's first in the transcript (observed)"
	case f.Turn1Prompt <= 0 || f.Turn2Prompt <= 0:
		return hooks.CapUntested, "the transcript has no prompt size for the turns, so the cache cannot be checked"
	case f.Turn2Read < f.Turn1Prompt-contextCacheSlack:
		return hooks.CapNo, fmt.Sprintf("the turn with the test text read %s of the previous %s prompt tokens from the cache, so adding it broke the cached prefix (observed)",
			tok(f.Turn2Read), tok(f.Turn1Prompt))
	case f.Turn3Read < f.Turn2Prompt-contextCacheSlack:
		return hooks.CapNo, fmt.Sprintf("the turn after the test text read %s of the previous %s prompt tokens from the cache, so the text did not stay in place (observed)",
			tok(f.Turn3Read), tok(f.Turn2Prompt))
	}
	ev := fmt.Sprintf("in your own session the test text landed after the previous turn and before the new turn's first request; that turn read %s of %s earlier prompt tokens from the cache and the next read %s of %s (observed)",
		tok(f.Turn2Read), tok(f.Turn1Prompt), tok(f.Turn3Read), tok(f.Turn2Prompt))
	if !f.ModelAllowed {
		ev += "; the session's model is not one hook_routine_steer acts on, which does not change this result"
	}
	return hooks.CapYes, ev
}

func tok(n int64) string { return strconv.FormatInt(n, 10) }
