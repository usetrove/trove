package probe

// The session check is the way to test hook rewriting without Trove starting any
// model request. `trove hooks verify` writes a settings file and prints a prompt;
// the USER starts their own `claude --settings <file>` session and pastes the
// prompt. Trove runs no agent, makes no network call and touches no login. The
// test hooks in that file rewrite one tool call per tool and record what the
// agent did. `trove hooks verify --finish` reads the record.
//
// The input check is as strong as a live probe: the PostToolUse hook is told what
// the tool really ran with. The output check cannot be observed by a hook, so it
// rests on the user's own statement of whether the model's reply contained the
// replacement text. The result is stored with method "session", and the evidence
// says so.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/hooks"
)

// SessionTTL is how long a verify session's hooks stay active.
const SessionTTL = time.Hour

type sessionFile struct {
	Armed   string   `json:"armed"`
	Expires string   `json:"expires"`
	Version string   `json:"agent_version,omitempty"`
	Tools   []string `json:"tools"`
}

// ArmOptions configure Arm.
type ArmOptions struct {
	Tools []string // default DefaultTools
	// HookCommand returns the program and arguments the agent runs for a test hook.
	// mode is "pre" or "post"; state is the state directory the hook must be given.
	// They are written in the agent's exec form (command plus args, no shell), which
	// the hooks reference documents and which avoids quoting differences between
	// Git Bash and PowerShell on Windows.
	HookCommand func(mode, state string) (command string, args []string)
	Version     string // the installed agent version, if known
	Now         func() time.Time
}

// Armed is what Arm prepared.
type Armed struct {
	State        string            `json:"state_dir"`
	SettingsPath string            `json:"settings_file"`
	Project      string            `json:"project_dir"`
	Expires      string            `json:"expires"`
	Prompts      map[string]string `json:"prompts"`
	Tools        []string          `json:"tools"`
}

// Arm prepares a verify session in state: one plan and one prompt per tool, a
// small project of synthetic files, and the settings file to start the session
// with. It runs nothing.
func Arm(state string, o ArmOptions) (Armed, error) {
	if o.HookCommand == nil {
		return Armed{}, errors.New("internal error: verify needs a hook command")
	}
	if len(o.Tools) == 0 {
		o.Tools = DefaultTools
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if err := os.RemoveAll(state); err != nil {
		return Armed{}, err
	}
	project := filepath.Join(state, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		return Armed{}, err
	}
	now := o.Now().UTC()
	a := Armed{State: state, Project: project, Expires: now.Add(SessionTTL).Format(time.RFC3339), Prompts: map[string]string{}}
	var pre, post []any
	for i, t := range o.Tools {
		if !toolNameRe.MatchString(t) {
			return Armed{}, fmt.Errorf("%q is not a tool name Trove can test", t)
		}
		nonce := fmt.Sprintf("%x", (now.UnixNano()+int64(i)*7919)&0xffffff)
		p := plan{Tool: t, Nonce: nonce, MarkerIn: "TROVE_PROBE_IN_" + nonce, MarkerOut: "TROVE_PROBE_OUT_" + nonce}
		prompt, err := setup(project, p)
		if err != nil {
			return Armed{}, err
		}
		pb, _ := json.Marshal(p)
		if err := os.WriteFile(filepath.Join(state, "plan-"+t+".json"), pb, 0o600); err != nil {
			return Armed{}, err
		}
		a.Prompts[t] = prompt
		a.Tools = append(a.Tools, t)
		hook := func(mode string) []any {
			cmd, args := o.HookCommand(mode, state)
			return []any{map[string]any{"type": "command", "command": cmd, "args": args, "timeout": 10}}
		}
		pre = append(pre, map[string]any{"matcher": t, "hooks": hook("pre")})
		post = append(post, map[string]any{"matcher": t, "hooks": hook("post")})
	}
	sf, _ := json.Marshal(sessionFile{Armed: now.Format(time.RFC3339), Expires: a.Expires, Version: o.Version, Tools: a.Tools})
	if err := os.WriteFile(filepath.Join(state, "session.json"), sf, 0o600); err != nil {
		return Armed{}, err
	}
	settings, _ := json.MarshalIndent(map[string]any{"hooks": map[string]any{"PreToolUse": pre, "PostToolUse": post}}, "", "  ")
	a.SettingsPath = filepath.Join(state, "settings.json")
	if err := os.WriteFile(a.SettingsPath, settings, 0o600); err != nil {
		return Armed{}, err
	}
	return a, nil
}

// FinishOptions configure Finish.
type FinishOptions struct {
	// SawOutput maps a tool to what the user said about the model's reply: true if
	// it contained the replacement text, false if it did not. A tool missing from
	// the map was not answered, and its output result stays untested.
	SawOutput map[string]bool
	Version   string
	Now       func() time.Time
}

// Finish reads what the test hooks recorded in the user's session and returns a
// capability entry for each tool that was armed. It runs nothing.
func Finish(state string, o FinishOptions) ([]hooks.Capability, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	b, err := os.ReadFile(filepath.Join(state, "session.json")) //nolint:gosec // Trove's own state dir
	if err != nil {
		return nil, errors.New("no verify session is waiting. Run `trove hooks verify` first")
	}
	var s sessionFile
	if json.Unmarshal(b, &s) != nil {
		return nil, errors.New("the verify session file is damaged. Run `trove hooks verify` again")
	}
	ver := o.Version
	if ver == "" {
		ver = s.Version
	}
	if ver == "" {
		ver = "unknown"
	}
	now := o.Now().UTC().Format(time.RFC3339)
	all := readSeen(state)
	var out []hooks.Capability
	for _, t := range s.Tools {
		c := hooks.Capability{Agent: "claude", AgentVersion: ver, Tool: t, Method: "session", ProbedAt: now,
			InputRewrite: hooks.CapUntested, OutputRewrite: hooks.CapUntested}
		var ev []seen
		for _, e := range all {
			if e.Tool == t {
				ev = append(ev, e)
			}
		}
		if len(ev) == 0 {
			c.Evidence = "the test hooks never ran: " + t + " was not called in the session"
			out = append(out, c)
			continue
		}
		pb, err := os.ReadFile(filepath.Join(state, "plan-"+t+".json")) //nolint:gosec // Trove's own state dir
		var p plan
		if err != nil || json.Unmarshal(pb, &p) != nil {
			c.Evidence = "the plan for " + t + " is missing; run `trove hooks verify` again"
			out = append(out, c)
			continue
		}
		saw, answered := o.SawOutput[t]
		reply := ""
		if saw {
			reply = p.MarkerOut
		}
		judge(&c, p, reply, ev)
		switch {
		case !answered && c.OutputRewrite != hooks.CapUntested:
			// judge read an empty reply as "ignored"; without an answer that is unknown.
			c.OutputRewrite = hooks.CapUntested
			c.Evidence += "; you did not say whether the reply contained " + p.MarkerOut + ", so the output result is untested"
		case answered && c.OutputRewrite != hooks.CapUntested:
			c.Evidence += " (reported by you, not observed by Trove)"
		}
		c.Evidence = strings.TrimPrefix(c.Evidence, "; ")
		out = append(out, c)
	}
	return out, nil
}

// Disarm removes a verify session, so its hooks and settings file are gone.
func Disarm(state string) error { return os.RemoveAll(state) }
