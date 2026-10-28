// Package setup points supported agents at the Trove gateway and reverses it.
//
// Each change is planned first (nothing is written), then applied only with the
// caller's consent: the original file is backed up under <dataDir>/backups and
// the change is recorded in <dataDir>/changes.json so Undo can reverse it
// exactly. Trove never edits shell rc files here and never touches credentials.
//
// Verified against official docs on 2026-10-28:
//
//	Claude Code: ANTHROPIC_BASE_URL in the "env" block of ~/.claude/settings.json
//	  (code.claude.com/docs/en/llm-gateway-connect). The settings-file value wins
//	  over a shell export. The API key stays in ANTHROPIC_API_KEY, which Trove
//	  does not read or write.
//	Codex CLI: ~/.codex/config.toml with model_provider and [model_providers.<id>]
//	  keys base_url, env_key, wire_api, supports_websockets
//	  (learn.chatgpt.com/docs/config-file/config-advanced and
//	  developers.openai.com/codex/config-reference; supports_websockets=false is
//	  taken from the config reference and user reports, not an official how-to).
package setup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Agent identifiers used in plans and the manifest.
const (
	ClaudeCode = "claude_code"
	Codex      = "codex"
)

const (
	baseURLKey   = "ANTHROPIC_BASE_URL"
	providerID   = "trove"
	markerOpen   = "# >>> trove (managed by `trove setup`; remove with `trove undo`)"
	markerClose  = "# <<< trove"
	manifestFile = "changes.json"
)

// Edit is one planned file change. Nothing has been written when it is returned.
type Edit struct {
	Agent   string `json:"agent"`
	Path    string `json:"path"`
	Show    string `json:"show"`    // the exact lines that will be added
	Summary string `json:"summary"` // one plain-English sentence
	Note    string `json:"note,omitempty"`
	NoOp    bool   `json:"no_op,omitempty"` // already configured

	old     []byte // nil if the file does not exist
	updated []byte
	value   string // the value we set, for surgical undo
}

// GatewayURL is the base URL an agent should use for a provider route.
func GatewayURL(port int, route string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, route)
}

// PlanClaudeCode plans the settings.json change. claudeDir is the Claude Code
// config directory (~/.claude or CLAUDE_CONFIG_DIR).
func PlanClaudeCode(claudeDir string, port int) (*Edit, error) {
	path := filepath.Join(claudeDir, "settings.json")
	want := GatewayURL(port, "/anthropic")
	old, err := readOptional(path)
	if err != nil {
		return nil, err
	}
	doc := map[string]json.RawMessage{}
	if old != nil {
		if err := json.Unmarshal(old, &doc); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON (%v). Fix or move that file, then run `trove setup` again; Trove will not overwrite a file it cannot read", path, err)
		}
	}
	env := map[string]json.RawMessage{}
	if raw, ok := doc["env"]; ok {
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("the \"env\" entry in %s is not an object, so Trove cannot add to it. Fix that file, then run `trove setup` again", path)
		}
	}
	e := &Edit{Agent: ClaudeCode, Path: path, value: want, old: old,
		Show:    fmt.Sprintf("\"env\": { \"%s\": \"%s\" }", baseURLKey, want),
		Summary: "Point Claude Code at the Trove gateway by setting " + baseURLKey + " in its settings file.",
		Note: "Claude Code must authenticate with an API key (ANTHROPIC_API_KEY). If you are signed in with a claude.ai subscription and have no API key set, " +
			"the gateway will reject the requests with a clear message; run `trove undo` to go back.",
	}
	if raw, ok := env[baseURLKey]; ok {
		var cur string
		_ = json.Unmarshal(raw, &cur)
		if cur == want {
			e.NoOp = true
			return e, nil
		}
		return nil, fmt.Errorf("%s already sets %s to %q. Trove will not overwrite it (it may be your organization's gateway). "+
			"Remove that line yourself if you want Trove in front, or set upstreams.anthropic in ~/.trove/config.toml to that address", path, baseURLKey, cur)
	}
	env[baseURLKey], _ = json.Marshal(want)
	doc["env"], _ = json.Marshal(env)
	out, err := marshalIndent(doc)
	if err != nil {
		return nil, err
	}
	e.updated = out
	return e, nil
}

// marshalIndent writes keys in sorted order. Key order and spacing in the
// user's file may change; the original is kept in the backup.
func marshalIndent(doc map[string]json.RawMessage) ([]byte, error) {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// PlanCodex plans the config.toml change using a custom provider that keeps
// API-key auth (env_key) and HTTP/SSE transport (supports_websockets = false).
func PlanCodex(codexDir string, port int) (*Edit, error) {
	path := filepath.Join(codexDir, "config.toml")
	old, err := readOptional(path)
	if err != nil {
		return nil, err
	}
	base := GatewayURL(port, "/openai/v1")
	top := markerOpen + "\nmodel_provider = \"" + providerID + "\"\n" + markerClose + "\n"
	table := markerOpen + "\n[model_providers." + providerID + "]\n" +
		"name = \"Trove local gateway\"\n" +
		"base_url = \"" + base + "\"\n" +
		"env_key = \"OPENAI_API_KEY\"\n" +
		"wire_api = \"responses\"\n" +
		"supports_websockets = false\n" + markerClose + "\n"
	e := &Edit{Agent: Codex, Path: path, value: base, old: old,
		Show:    top + "...\n" + table,
		Summary: "Point Codex CLI at the Trove gateway with a custom provider named \"" + providerID + "\".",
		Note: "Codex will use API-key auth from OPENAI_API_KEY (not a ChatGPT login) over HTTP/SSE. " +
			"Known side effect: Codex ties saved sessions to the provider, so `codex resume` may not list older sessions while this is on; " +
			"resuming by session id still works.",
	}
	if bytes.Contains(old, []byte(markerOpen)) {
		e.NoOp = true
		return e, nil
	}
	if old != nil {
		var cur map[string]any
		if err := toml.Unmarshal(old, &cur); err != nil {
			return nil, fmt.Errorf("%s is not valid TOML (%v). Fix or move that file, then run `trove setup` again; Trove will not overwrite a file it cannot read", path, err)
		}
		if v, ok := cur["model_provider"]; ok {
			return nil, fmt.Errorf("%s already sets model_provider to %v. Trove will not override your provider choice. "+
				"Remove that line (or point its base_url at %s yourself), then run `trove setup` again", path, v, base)
		}
		if mp, ok := cur["model_providers"].(map[string]any); ok {
			if _, dup := mp[providerID]; dup {
				return nil, fmt.Errorf("%s already defines [model_providers.%s]. Rename or remove it, then run `trove setup` again", path, providerID)
			}
		}
	}
	body := string(old)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if body != "" {
		body += "\n"
	}
	updated := top + "\n" + body + table // a top-level key must precede every [table]
	var check map[string]any
	if err := toml.Unmarshal([]byte(updated), &check); err != nil {
		return nil, fmt.Errorf("internal check failed: the planned %s would not be valid TOML (%v); nothing was changed", path, err)
	}
	e.updated = []byte(updated)
	return e, nil
}

// Change is one applied edit recorded for undo.
type Change struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	Path      string    `json:"path"`
	Created   bool      `json:"created"` // the file did not exist before
	Backup    string    `json:"backup,omitempty"`
	SHAAfter  string    `json:"sha256_after"`
	Value     string    `json:"value"`
	AppliedAt time.Time `json:"applied_at"`
}

// Apply writes edits (skipping no-ops), backing up each original and recording
// every change. The caller must already have the user's consent.
func Apply(dataDir string, edits []*Edit, now time.Time) ([]Change, error) {
	changes, err := LoadChanges(dataDir)
	if err != nil {
		return nil, err
	}
	var applied []Change
	for _, e := range edits {
		if e == nil || e.NoOp {
			continue
		}
		c := Change{ID: now.UTC().Format("20060102T150405.000000000Z") + "-" + e.Agent, Agent: e.Agent, Path: e.Path,
			Created: e.old == nil, Value: e.value, AppliedAt: now.UTC()}
		if e.old != nil {
			bdir := filepath.Join(dataDir, "backups")
			if err := os.MkdirAll(bdir, 0o700); err != nil {
				return applied, fmt.Errorf("cannot create %s: %v", bdir, err)
			}
			c.Backup = filepath.Join(bdir, c.ID+"-"+filepath.Base(e.Path)+".bak")
			if err := os.WriteFile(c.Backup, e.old, 0o600); err != nil {
				return applied, fmt.Errorf("cannot back up %s: %v. Nothing was changed", e.Path, err)
			}
		}
		if err := os.MkdirAll(filepath.Dir(e.Path), 0o755); err != nil { //nolint:gosec // agent config directories are user-owned and conventionally 0755
			return applied, fmt.Errorf("cannot create %s: %v", filepath.Dir(e.Path), err)
		}
		if err := writeAtomic(e.Path, e.updated); err != nil {
			return applied, fmt.Errorf("cannot write %s: %v. Your original is untouched", e.Path, err)
		}
		c.SHAAfter = sum(e.updated)
		changes = append(changes, c)
		applied = append(applied, c)
		if err := saveChanges(dataDir, changes); err != nil { // record immediately so a later failure is still undoable
			return applied, err
		}
	}
	return applied, nil
}

// LoadChanges reads the manifest (empty if none).
func LoadChanges(dataDir string) ([]Change, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, manifestFile)) //nolint:gosec // inside the data dir
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v", manifestFile, err)
	}
	var cs []Change
	if err := json.Unmarshal(b, &cs); err != nil {
		return nil, fmt.Errorf("%s in your Trove data directory is damaged (%v). Your agents' original files are in the backups folder next to it", manifestFile, err)
	}
	return cs, nil
}

func saveChanges(dataDir string, cs []Change) error {
	p := filepath.Join(dataDir, manifestFile)
	if len(cs) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	return writeAtomic(p, b)
}

// UndoResult describes what happened to one recorded change.
type UndoResult struct {
	Agent  string `json:"agent"`
	Path   string `json:"path"`
	Action string `json:"action"` // restored, removed_file, removed_our_lines, already_gone, left_alone
	Detail string `json:"detail"`
}

// Undo reverses every recorded change, newest first. If the file is exactly
// as Trove left it, the backup is restored byte for byte (or the file removed
// if Trove created it). If you have edited it since, only Trove's own lines
// are removed and the rest is left alone.
func Undo(dataDir string) ([]UndoResult, error) {
	cs, err := LoadChanges(dataDir)
	if err != nil {
		return nil, err
	}
	var out []UndoResult
	for len(cs) > 0 {
		c := cs[len(cs)-1]
		r, err := undoOne(c)
		if err != nil {
			_ = saveChanges(dataDir, cs) // keep what is left so a retry can finish
			return out, err
		}
		out = append(out, r)
		cs = cs[:len(cs)-1]
		if err := saveChanges(dataDir, cs); err != nil {
			return out, err
		}
	}
	return out, nil
}

func undoOne(c Change) (UndoResult, error) {
	res := UndoResult{Agent: c.Agent, Path: c.Path}
	cur, err := readOptional(c.Path)
	if err != nil {
		return res, err
	}
	if cur == nil {
		res.Action, res.Detail = "already_gone", "the file no longer exists; nothing to undo"
		return res, nil
	}
	if sum(cur) == c.SHAAfter { // untouched since setup: exact reversal
		if c.Created {
			if err := os.Remove(c.Path); err != nil {
				return res, fmt.Errorf("cannot remove %s: %v", c.Path, err)
			}
			res.Action, res.Detail = "removed_file", "removed the file Trove created"
			return res, nil
		}
		orig, err := os.ReadFile(c.Backup) //nolint:gosec // backup path recorded by Trove
		if err != nil {
			return res, fmt.Errorf("cannot read the backup %s: %v. Remove the Trove lines from %s by hand", c.Backup, err, c.Path)
		}
		if err := writeAtomic(c.Path, orig); err != nil {
			return res, fmt.Errorf("cannot restore %s: %v", c.Path, err)
		}
		res.Action, res.Detail = "restored", "restored your original file from the backup"
		return res, nil
	}
	// The file changed since setup: remove only our lines.
	var next []byte
	switch c.Agent {
	case ClaudeCode:
		next, err = stripClaude(cur, c.Value)
	case Codex:
		next = stripMarked(cur)
	default:
		res.Action, res.Detail = "left_alone", "unknown agent in the manifest"
		return res, nil
	}
	if err != nil {
		res.Action, res.Detail = "left_alone", err.Error()
		return res, nil
	}
	if bytes.Equal(next, cur) {
		res.Action, res.Detail = "left_alone", "Trove's lines were already removed"
		return res, nil
	}
	if err := writeAtomic(c.Path, next); err != nil {
		return res, fmt.Errorf("cannot update %s: %v", c.Path, err)
	}
	res.Action, res.Detail = "removed_our_lines", "you had edited the file since setup, so only Trove's lines were removed"
	return res, nil
}

func stripClaude(cur []byte, value string) ([]byte, error) {
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(cur, &doc); err != nil {
		return nil, errors.New("the file is no longer valid JSON; remove the " + baseURLKey + " line by hand")
	}
	env := map[string]json.RawMessage{}
	_ = json.Unmarshal(doc["env"], &env)
	var got string
	_ = json.Unmarshal(env[baseURLKey], &got)
	if got != value {
		return cur, nil // changed by you since; not ours to remove
	}
	delete(env, baseURLKey)
	if len(env) == 0 {
		delete(doc, "env")
	} else {
		doc["env"], _ = json.Marshal(env)
	}
	return marshalIndent(doc)
}

// stripMarked removes every block between Trove's marker lines.
func stripMarked(cur []byte) []byte {
	var out []string
	skip := false
	for _, l := range strings.SplitAfter(string(cur), "\n") {
		t := strings.TrimRight(l, "\r\n")
		switch {
		case t == markerOpen:
			skip = true
		case skip && t == markerClose:
			skip = false
		case !skip:
			out = append(out, l)
		}
	}
	s := strings.Join(out, "")
	s = strings.TrimLeft(s, "\r\n") // the blank line we added after the top block
	return []byte(strings.TrimRight(s, "\r\n") + trailingNewline(s))
}

func trailingNewline(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return "\n"
}

func readOptional(path string) ([]byte, error) {
	b, err := os.ReadFile(path) //nolint:gosec // agent config path chosen by the user's own setup
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v. Check its permissions", path, err)
	}
	if b == nil {
		b = []byte{}
	}
	return b, nil
}

func writeAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".trove-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Updated returns the bytes that Apply would write (for tests and previews).
func (e *Edit) Updated() []byte { return e.updated }
