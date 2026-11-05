package optimize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/usetrove/trove/internal/config"
)

// This file holds the two optimizers that rewrite a tool call's input before it
// runs. Both only LIMIT or QUIET output; neither changes what a call does.

// ReadLimitLines is the line limit added to a large Read that has none. The
// agent can read more with offset and limit, or use trove_read.
const ReadLimitLines = 400

// ReadLimitMinBytes is the file size from which an unlimited Read gets a limit.
// Smaller files are cheap to read whole.
const ReadLimitMinBytes = 40_000

// setField returns raw (a JSON object) with key set to value, every other field
// untouched. updatedInput replaces the whole input, so unchanged fields must be
// carried over exactly.
func setFields(raw json.RawMessage, set map[string]any) (json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return raw, false
	}
	for k, v := range set {
		b, err := json.Marshal(v)
		if err != nil {
			return raw, false
		}
		m[k] = b
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw, false
	}
	return out, true
}

// ReadLimit is the hook_read_limit optimizer. A Read of a large file that sets
// neither offset nor limit gets a line limit, and the agent is told how to read
// more. Repeat reads are left to RepeatRead, which runs first.
type ReadLimit struct{}

// Name implements Base.
func (ReadLimit) Name() string { return config.OptimizerHookReadLimit }

// Tier implements Base: the agent can read the rest with offset and limit.
func (ReadLimit) Tier() Tier { return TierRecoverable }

// Points implements Capable.
func (ReadLimit) Points() []Point { return []Point{PointToolInput} }

// OverheadTokens implements Capable: the note is sent only when it acts, and its
// size is netted out of the estimate below.
func (ReadLimit) OverheadTokens() int { return 0 }

// Support implements SupportOverrider.
func (ReadLimit) Support(a Adapter) (Support, bool) {
	if a == AdapterGateway {
		return Support{LevelImpossible, "the limit has to be set before the Read runs; the gateway only sees finished history"}, true
	}
	return Support{}, false
}

// readLimitNote is what the agent is told; its size is subtracted from the saving.
func readLimitNote(size int64) string {
	return fmt.Sprintf("Trove: this Read was limited to the first %d lines of a file of about %d KB. Read the rest with offset and limit (or trove_read, if available).", ReadLimitLines, size/1000)
}

// TransformInput implements ToolInputOptimizer.
func (ReadLimit) TransformInput(_ context.Context, call ToolCall, s Session) (ToolCall, Report) {
	if call.Name != "Read" {
		return call, Report{}
	}
	a, ok := parseReadArgs(call.Input)
	if !ok || a.Offset != 0 || a.Limit != 0 {
		return call, Report{}
	}
	fs, ok := StatFile(a.FilePath, 0, false)
	if !ok || fs.Size < ReadLimitMinBytes {
		return call, Report{}
	}
	kept := int64(ReadLimitLines * bytesPerLine)
	note := readLimitNote(fs.Size)
	saved := fs.Size - kept - int64(len(note))
	if saved <= 0 {
		return call, Report{}
	}
	rep := Report{Name: config.OptimizerHookReadLimit, BytesRemoved: int(saved), TokensRemoved: EstTokens(saved)}
	if s.Mode == config.StateShadow {
		rep.Decision = &Decision{Kind: KindWouldRewrite, Bytes: saved, Tokens: rep.TokensRemoved}
		return call, rep
	}
	in, ok := setFields(call.Input, map[string]any{"limit": ReadLimitLines})
	if !ok {
		return call, Report{}
	}
	rep.Decision = &Decision{Kind: KindRewritten, Bytes: saved, Tokens: rep.TokensRemoved, Message: note}
	return ToolCall{Name: call.Name, Input: in}, rep
}

// QuietRule is one allowlisted rewrite: when a command is exactly Prefix followed
// by plain arguments, Add is appended. Verified is true only when the tool's
// official documentation says the flag limits output without hiding errors or
// changing behaviour; an unverified rule only ever runs in shadow.
type QuietRule struct {
	Prefix     []string
	Add        []string
	Skip       []string // if any of these words is present the command is left alone
	Verified   bool
	Source     string // documentation read
	VerifiedAt string // date it was read
	Note       string
}

// QuietRules is the allowlist, in match order. Add an entry only with a source
// that says the flag changes output and nothing else.
var QuietRules = []QuietRule{
	{Prefix: []string{"pip", "install"}, Add: []string{"-q"}, Skip: []string{"-q", "--quiet", "-v", "--verbose", "--progress-bar"},
		Verified: true, Source: "https://pip.pypa.io/en/stable/cli/pip/", VerifiedAt: "2026-11-04",
		Note: "docs: -q gives less output and maps to the WARNING log level, so warnings and errors remain"},
	{Prefix: []string{"pip3", "install"}, Add: []string{"-q"}, Skip: []string{"-q", "--quiet", "-v", "--verbose", "--progress-bar"},
		Verified: true, Source: "https://pip.pypa.io/en/stable/cli/pip/", VerifiedAt: "2026-11-04",
		Note: "same option as pip install"},
	{Prefix: []string{"npm", "install"}, Add: []string{"--quiet"}, Skip: []string{"-q", "--quiet", "-s", "--silent", "-d", "--loglevel", "--verbose"},
		Verified: true, Source: "https://docs.npmjs.com/cli/v10/using-npm/config", VerifiedAt: "2026-11-04",
		Note: "docs: --quiet is shorthand for --loglevel warn"},
	{Prefix: []string{"npm", "ci"}, Add: []string{"--quiet"}, Skip: []string{"-q", "--quiet", "-s", "--silent", "-d", "--loglevel", "--verbose"},
		Verified: true, Source: "https://docs.npmjs.com/cli/v10/using-npm/config", VerifiedAt: "2026-11-04",
		Note: "docs: --quiet is shorthand for --loglevel warn"},
	// Not verified: the pages read do not say that errors stay visible, so these
	// only estimate (shadow). Promote one by reading the page that says so.
	{Prefix: []string{"cargo", "build"}, Add: []string{"--quiet"}, Skip: []string{"-q", "--quiet", "-v", "--verbose"},
		Source: "https://doc.rust-lang.org/cargo/commands/cargo-build.html", VerifiedAt: "2026-11-04",
		Note: "docs: do not print cargo log messages; they do not say compiler errors remain"},
	{Prefix: []string{"cargo", "check"}, Add: []string{"--quiet"}, Skip: []string{"-q", "--quiet", "-v", "--verbose"},
		Source: "https://doc.rust-lang.org/cargo/commands/cargo-check.html", VerifiedAt: "2026-11-04",
		Note: "docs: do not print cargo log messages; they do not say compiler errors remain"},
	{Prefix: []string{"pytest"}, Add: []string{"-q"}, Skip: []string{"-q", "--quiet", "-v", "--verbose", "-s"},
		Source: "https://docs.pytest.org/en/stable/", VerifiedAt: "2026-11-04",
		Note: "the page that documents -q was not read"},
}

// plainWord reports whether w is made only of characters that mean the same
// thing to every shell. Anything else (quotes, $, globs, redirects, pipes,
// separators, escapes, braces) makes the whole command ineligible.
func plainWord(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_-./=:@+,", r):
		default:
			return false
		}
	}
	return true
}

// QuietRewrite returns the quieter version of command and the rule used. ok is
// false when the command is not on the allowlist or is not simple enough to be
// sure of. The result is the original words plus the rule's Add words at the end.
func QuietRewrite(command string) (string, QuietRule, bool) {
	words := strings.Fields(command)
	// strings.Fields splits on any whitespace, so a newline would silently turn a
	// two-command script into one command line; refuse those outright.
	if len(words) == 0 || strings.ContainsAny(command, "\n\r") || !plainWordsOnly(words) {
		return "", QuietRule{}, false
	}
	for _, w := range words {
		if w == "--" { // what follows is not an option; appending would change meaning
			return "", QuietRule{}, false
		}
	}
	for _, r := range QuietRules {
		if len(words) < len(r.Prefix) {
			continue
		}
		match := true
		for i, p := range r.Prefix {
			match = match && words[i] == p
		}
		if !match {
			continue
		}
		for _, w := range words[len(r.Prefix):] {
			for _, s := range r.Skip {
				if w == s || strings.HasPrefix(w, s+"=") {
					return "", QuietRule{}, false
				}
			}
		}
		return strings.Join(append(append([]string{}, words...), r.Add...), " "), r, true
	}
	return "", QuietRule{}, false
}

func plainWordsOnly(words []string) bool {
	for _, w := range words {
		if !plainWord(w) {
			return false
		}
	}
	return true
}

// QuietCommands is the hook_quiet_commands optimizer.
type QuietCommands struct{}

// Name implements Base.
func (QuietCommands) Name() string { return config.OptimizerHookQuietCommands }

// Tier implements Base: it can only hide output the agent could ask for again by
// running the command without the flag.
func (QuietCommands) Tier() Tier { return TierRecoverable }

// Points implements Capable.
func (QuietCommands) Points() []Point { return []Point{PointToolInput} }

// OverheadTokens implements Capable: it adds a flag to a command, no text to the context.
func (QuietCommands) OverheadTokens() int { return 0 }

// Support implements SupportOverrider.
func (QuietCommands) Support(a Adapter) (Support, bool) {
	if a == AdapterGateway {
		return Support{LevelImpossible, "the flag has to be added before the command runs; the gateway only sees finished history"}, true
	}
	return Support{}, false
}

// TransformInput implements ToolInputOptimizer. Only the command field changes.
// The savings are not estimated: how much a flag removes depends on the command,
// so only the count is recorded.
func (QuietCommands) TransformInput(_ context.Context, call ToolCall, s Session) (ToolCall, Report) {
	if call.Name != "Bash" {
		return call, Report{}
	}
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(call.Input, &a) != nil {
		return call, Report{}
	}
	quiet, rule, ok := QuietRewrite(a.Command)
	if !ok {
		return call, Report{}
	}
	rep := Report{Name: config.OptimizerHookQuietCommands}
	if s.Mode == config.StateShadow || !rule.Verified {
		rep.Decision = &Decision{Kind: KindWouldRewrite}
		return call, rep
	}
	// The agent running the same command again, as first written, means the quiet
	// version did not give it what it needed. Count that once per command and stop
	// quieting it, so the agent is never made to run it twice to get its output.
	var key string
	if s.Ledger != nil {
		key = "q:" + commandKey(a.Command)
		if ent, seen := s.Ledger.Get(key); seen {
			if ent.BlockedSum != "" {
				return call, Report{} // already counted
			}
			ent.BlockedSum = "counted"
			s.Ledger.Put(key, ent)
			if err := s.Ledger.Save(); err != nil {
				return call, Report{Err: "ledger"}
			}
			return call, Report{Name: config.OptimizerHookQuietCommands, Decision: &Decision{Kind: KindReissued}}
		}
	}
	in, ok := setFields(call.Input, map[string]any{"command": quiet})
	if !ok {
		return call, Report{}
	}
	if s.Ledger != nil {
		s.Ledger.Put(key, ReadEntry{Prompt: s.Ledger.Prompts(), At: s.Now.Unix()})
		if err := s.Ledger.Save(); err != nil {
			return call, Report{Err: "ledger"} // if the rewrite cannot be remembered, do not make it
		}
	}
	rep.Decision = &Decision{Kind: KindRewritten}
	return ToolCall{Name: call.Name, Input: in}, rep
}

// commandKey identifies a command in the session ledger by a hash, never its text.
func commandKey(command string) string {
	h := sha256.Sum256([]byte(command))
	return hex.EncodeToString(h[:])[:16]
}

// TrimmedKey is the ledger key under which an output the trimmer shortened is
// remembered, so a later trove_fetch_full for it can be attributed to the trimmer.
func TrimmedKey(id string) string { return "t:" + id }
