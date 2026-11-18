package fixer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/usetrove/trove/internal/config"
)

// RetypedCommand offers a shell command the agent keeps typing out as a named
// trove_run command in the project's .trove/commands.toml (docs/fixer.md).
const RetypedCommand = "retyped_command"

const (
	// commandsRel is trove_run's allowlist inside a project (internal/mcp).
	commandsRel = ".trove/commands.toml"
	// minRetypedBytes: shorter commands cost too little to be worth a name.
	minRetypedBytes = 20
	// maxRetypedFixes caps how many commands one run of `trove fix` offers.
	maxRetypedFixes = 10
	// maxCommandsBytes matches the size trove_run accepts for the file.
	maxCommandsBytes = 64 << 10
)

// runNameRe is the command-name rule trove_run enforces (internal/mcp/run.go).
var runNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

// shellMeta are characters that only mean something to a shell. trove_run runs
// an argument list directly and never through a shell, so a command that uses
// any of them cannot be copied into commands.toml as it is.
const shellMeta = "|&;<>()$`\\*?[]{}~!#\n\r"

// shellBuiltins only work inside a shell.
var shellBuiltins = map[string]bool{"cd": true, "export": true, "source": true, ".": true, "alias": true, "set": true, "eval": true, "exec": true, "unset": true}

// retyped is one command seen in the history, held in memory for one run.
type retyped struct {
	cmd      string
	count    int
	sessions map[string]bool
}

// commandKey groups commands that differ only in spacing. It is a hash, so the
// grouping never needs the text kept anywhere.
func commandKey(cmd string) string {
	h := sha256.Sum256([]byte(strings.Join(strings.Fields(cmd), " ")))
	return hex.EncodeToString(h[:])
}

// splitArgv splits a command into its arguments the way a shell would, for the
// simple cases only: words separated by spaces or tabs, with '...' and "..."
// quoting. ok is false when the command needs a shell to mean what it says.
func splitArgv(cmd string) (argv []string, ok bool) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" || strings.ContainsAny(cmd, shellMeta) {
		return nil, false
	}
	var cur strings.Builder
	inWord := false
	var quote rune
	for _, r := range cmd {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			if inWord {
				argv = append(argv, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	if inWord {
		argv = append(argv, cur.String())
	}
	if len(argv) == 0 || shellBuiltins[argv[0]] || strings.Contains(argv[0], "=") {
		return nil, false
	}
	return argv, true
}

// commandName derives a trove_run name from the program and its first plain
// arguments, unique among taken.
func commandName(argv []string, cmd string, taken map[string]bool) string {
	clean := func(s string) string {
		s = filepath.Base(filepath.ToSlash(s))
		s = strings.TrimSuffix(s, filepath.Ext(s))
		var b strings.Builder
		for _, r := range strings.ToLower(s) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
				b.WriteRune(r)
			default:
				b.WriteByte('-')
			}
		}
		return strings.Trim(b.String(), "-_")
	}
	var parts []string
	for i, a := range argv {
		if i > 0 && (strings.HasPrefix(a, "-") || strings.ContainsAny(a, " \t") || len(parts) == 3) {
			continue
		}
		if c := clean(a); c != "" {
			parts = append(parts, c)
		}
	}
	base := strings.Join(parts, "-")
	if len(base) > 28 {
		base = strings.TrimRight(base[:28], "-_")
	}
	if !runNameRe.MatchString(base) {
		base = "cmd-" + commandKey(cmd)[:8]
	}
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	taken[name] = true
	return name
}

// tomlString writes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

type runEntry struct {
	Run []string `toml:"run"`
}

// readCommands reads the project's commands.toml: its bytes (nil if absent) and entries.
func readCommands(path string) ([]byte, map[string]runEntry, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the project's own trove_run allowlist
	if errors.Is(err, os.ErrNotExist) {
		return nil, map[string]runEntry{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if len(b) > maxCommandsBytes {
		return nil, nil, fmt.Errorf("%s is larger than %d bytes, which trove_run refuses; shorten it first", commandsRel, maxCommandsBytes)
	}
	cmds, err := parseCommands(b)
	if err != nil {
		return nil, nil, fmt.Errorf("%s is not valid TOML (%v); fix it first, see docs/mcp.md", commandsRel, err)
	}
	return b, cmds, nil
}

// parseCommands parses commands.toml content; the map is never nil on success.
func parseCommands(b []byte) (map[string]runEntry, error) {
	var doc struct {
		Commands map[string]runEntry `toml:"commands"`
	}
	if err := toml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if doc.Commands == nil {
		doc.Commands = map[string]runEntry{}
	}
	return doc.Commands, nil
}

// listed reports whether argv is already one of the file's commands.
func listed(cmds map[string]runEntry, argv []string) bool {
	for _, c := range cmds {
		if slices.Equal(c.Run, argv) {
			return true
		}
	}
	return false
}

const commandsHeader = "# Commands trove_run may run (see docs/mcp.md). Each runs directly, never through a shell.\n" +
	"# trove_run uses this file only after you approve it with `trove mcp trust`.\n"

// appendCommand returns old with one [commands.<name>] table added.
func appendCommand(old []byte, name string, argv []string) []byte {
	var b strings.Builder
	if old == nil {
		b.WriteString(commandsHeader)
	} else {
		b.Write(old)
		if len(old) > 0 && old[len(old)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = tomlString(a)
	}
	fmt.Fprintf(&b, "\n[commands.%s]\nrun = [%s]\n", name, strings.Join(quoted, ", "))
	return []byte(b.String())
}

func preview(cmd string) string {
	cmd = strings.Join(strings.Fields(cmd), " ")
	if r := []rune(cmd); len(r) > 80 {
		return string(r[:77]) + "..."
	}
	return cmd
}

// detectRetyped offers commands from the project's Claude Code history that the
// agent typed out at least fixer.retyped_min_count times in at least
// fixer.retyped_min_sessions sessions. A plain command becomes an applicable fix
// that adds it to .trove/commands.toml; one that needs a shell is advisory.
// Applying it never lets trove_run run anything: the file still has to be
// approved with `trove mcp trust`. The command text is read from the history in
// memory and is never stored.
func detectRetyped(ctx context.Context, e *Env) ([]*Fix, error) {
	u := e.projectUsage(ctx)
	if !u.matched || e.ProjectDir == "" {
		return nil, nil
	}
	var cands []*retyped
	for _, r := range u.commands {
		if r.count >= e.Cfg.RetypedMinCount && len(r.sessions) >= e.Cfg.RetypedMinSessions && len(strings.TrimSpace(r.cmd)) >= minRetypedBytes {
			cands = append(cands, r)
		}
	}
	if len(cands) == 0 {
		return nil, nil
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].count != cands[j].count {
			return cands[i].count > cands[j].count
		}
		return commandKey(cands[i].cmd) < commandKey(cands[j].cmd)
	})
	path := filepath.Join(e.ProjectDir, filepath.FromSlash(commandsRel))
	old, existing, rerr := readCommands(path)
	taken := map[string]bool{}
	for n := range existing {
		taken[n] = true
	}
	var out []*Fix
	for _, r := range cands {
		if len(out) == maxRetypedFixes {
			break
		}
		argv, plain := splitArgv(r.cmd)
		if plain && listed(existing, argv) {
			continue // already a trove_run command
		}
		f := &Fix{ID: fixID(RetypedCommand, commandKey(r.cmd)), Kind: RetypedCommand, Agent: "claude_code", Path: path, Suggestion: true}
		f.Evidence = []string{
			fmt.Sprintf("typed out %d times in %d of %d sessions in the last %d days (Claude Code history)", r.count, len(r.sessions), u.sessions, e.Cfg.LookbackDays),
			"command: " + preview(r.cmd),
		}
		if !plain {
			name := "cmd-" + commandKey(r.cmd)[:8]
			f.Title = "Make a retyped shell command a trove_run command"
			f.Why = "The agent keeps writing this command out. It uses shell syntax, and trove_run never runs a shell, so Trove cannot add it as it is."
			f.Advisory = fmt.Sprintf("Save the command as a script in the project, then add [commands.%s] with run = [\"<interpreter>\", \"<script>\"] to %s and approve it with `trove mcp trust`.", name, commandsRel)
			out = append(out, f)
			continue
		}
		name := commandName(argv, r.cmd, taken)
		f.Title = fmt.Sprintf("Add %q to trove_run's commands", name)
		f.Why = fmt.Sprintf("The agent keeps writing this command out. As the trove_run command %q it is called by name, and its output is shortened with the full text kept fetchable.", name)
		call := estTokens(len(fmt.Sprintf(`{"name":%q}`, name)))
		if per := estTokens(len(r.cmd)) - call; per > 0 && u.sessions > 0 {
			f.TokensPerSession = per * int64(r.count) / int64(u.sessions)
		}
		f.Notes = []string{
			"The estimate counts only the command text the agent stops writing; output shortening by trove_run is not included.",
			"trove_run needs Trove's MCP server (`trove mcp install`) and runs this file only after you approve it with `trove mcp trust`.",
			"Use is measured by the trove_run counter in Trove's hooks and shown by `trove fix`.",
		}
		switch {
		case rerr != nil:
			f.Blocked = rerr.Error()
		default:
			updated := appendCommand(old, name, argv)
			if got, err := parseCommands(updated); err != nil || !slices.Equal(got[name].Run, argv) {
				f.Blocked = "Trove could not add this command to " + commandsRel + " without changing what it means; add it yourself"
			} else {
				f.old, f.updated = old, updated
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// RunAdoption says whether the agent uses trove_run after retyped commands were
// added. Sessions come from Trove's hook counter (hook_events, kind "adopted"),
// which counts a session once the first time it calls trove_run; it covers
// every project, because hook events carry no project.
type RunAdoption struct {
	// Commands is how many retyped_command fixes are in place for this project.
	Commands int `json:"commands"`
	// AdoptedSessions is how many sessions used trove_run since the first was applied.
	AdoptedSessions int `json:"adopted_sessions"`
	// HookSessions is how many sessions the hooks saw in that time.
	HookSessions int    `json:"hook_sessions"`
	Label        string `json:"label"` // "measured"
	// Observable is false when the installed hooks do not count trove_run use; the counts are then 0.
	Observable bool `json:"observable"`
}

// RetypedAdoption reads the adoption of trove_run for the project's applied
// retyped_command fixes. It returns nil when none is in place or there is no database.
func RetypedAdoption(ctx context.Context, e *Env, observable bool) (*RunAdoption, error) {
	if e.DB == nil || e.ProjectDir == "" {
		return nil, nil
	}
	ph := pathHash(filepath.Join(e.ProjectDir, filepath.FromSlash(commandsRel)))
	var n int
	var since sql.NullInt64
	if err := e.DB.QueryRowContext(ctx, `SELECT COUNT(*), MIN(applied_at) FROM fixes WHERE kind = ? AND path_hash = ? AND reverted_at IS NULL`,
		RetypedCommand, ph).Scan(&n, &since); err != nil {
		return nil, err
	}
	if n == 0 || !since.Valid {
		return nil, nil
	}
	a := &RunAdoption{Commands: n, Label: "measured", Observable: observable}
	if !observable {
		return a, nil
	}
	if err := e.DB.QueryRowContext(ctx, `SELECT
		COUNT(DISTINCT CASE WHEN hook = ? AND kind = 'adopted' THEN session_key END),
		COUNT(DISTINCT session_key)
		FROM hook_events WHERE ts >= ? AND session_key NOT LIKE 'x-%'`,
		config.OptimizerHookLargeOutput, since.Int64).Scan(&a.AdoptedSessions, &a.HookSessions); err != nil {
		return nil, err
	}
	return a, nil
}
