package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/usetrove/trove/internal/mcp/root"
	"github.com/usetrove/trove/internal/optimize"
)

// Limits for trove_run.
const (
	commandsFile     = ".trove/commands.toml"
	maxCommandsFile  = 64 << 10
	defaultTimeout   = 300 * time.Second
	maxTimeout       = 3600 * time.Second
	maxCaptureBytes  = 8 << 20
	maxInlineBytes   = 12 << 10
	maxFailingListed = 20
	// Names that carry credentials or give access to them. A project command
	// should not inherit the agent's keys, cloud logins, the ssh/gpg agents or
	// database URLs. This is a denylist, so it can miss unusual names; it is
	// documented as a mitigation, not a guarantee.
	envDropPatternText = `(?i)(api[_-]?key|token|secret|password|passwd|credential|private[_-]?key|bearer|cookie|` +
		`^anthropic_|^openai_|^aws_|^azure_|^authorization|^ssh_auth_sock$|^gpg_agent_info$|^database_url$|_dsn$|(^|_)pat$|^npm_config__auth)`
)

var (
	nameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
	envDropRe = regexp.MustCompile(envDropPatternText)
	failNamed = regexp.MustCompile(`^\s*(?:--- FAIL: (\S+)|FAILED (\S+)|FAIL\s+(\S+)|[✗✘×●] (.+))`)
)

// Blob stores full command output so it can be fetched back.
type Blob interface {
	Put(data []byte) (string, error)
}

// RunArgs are the trove_run inputs.
type RunArgs struct {
	Name string `json:"name"`
}

// Command is one entry in .trove/commands.toml. Run is an argument list that is
// executed directly; no shell is ever involved.
type Command struct {
	Run        []string `toml:"run"`
	TimeoutSec int      `toml:"timeout_sec"`
}

type commandsDoc struct {
	Commands map[string]Command `toml:"commands"`
}

// LoadCommands reads the project's allowlist. A missing file is an empty
// allowlist, not an error.
func LoadCommands(r *root.Root) (map[string]Command, error) {
	cmds, _, err := PendingCommands(r)
	return cmds, err
}

// PendingCommands parses the project's file and returns the hash of the exact
// bytes it parsed, so approval and execution refer to the same content. The
// hash is empty when there is no file.
func PendingCommands(r *root.Root) (map[string]Command, string, error) {
	target, err := r.Resolve(commandsFile)
	if err != nil {
		if errors.Is(err, root.ErrOutside) {
			return nil, "", fmt.Errorf("%s leads outside the project, so it was ignored", commandsFile)
		}
		return nil, "", nil
	}
	b, err := os.ReadFile(target) //nolint:gosec // path was resolved and checked to be inside the jail
	if err != nil {
		return nil, "", fmt.Errorf("cannot read %s: %w", commandsFile, err)
	}
	if len(b) > maxCommandsFile {
		return nil, "", fmt.Errorf("%s is too large (over %d bytes)", commandsFile, maxCommandsFile)
	}
	var doc commandsDoc
	if err := toml.Unmarshal(b, &doc); err != nil {
		return nil, "", fmt.Errorf("%s is not valid: %v. Fix the file; see docs/mcp.md for the format", commandsFile, err)
	}
	for name, c := range doc.Commands {
		if !nameRe.MatchString(name) {
			return nil, "", fmt.Errorf("%s: command name %q is not allowed. Use letters, digits, - and _", commandsFile, name)
		}
		if len(c.Run) == 0 || strings.TrimSpace(c.Run[0]) == "" {
			return nil, "", fmt.Errorf("%s: command %q needs run = [\"program\", \"arg\", ...]", commandsFile, name)
		}
		if c.TimeoutSec < 0 || time.Duration(c.TimeoutSec)*time.Second > maxTimeout {
			return nil, "", fmt.Errorf("%s: command %q has timeout_sec outside 0-%d", commandsFile, name, int(maxTimeout.Seconds()))
		}
	}
	return doc.Commands, FileHash(b), nil
}

// Run implements trove_run. It runs only commands named in the project's
// allowlist, directly and not through a shell, and returns a condensed result.
// The full output is stored first; if it cannot be stored, the output is not
// shortened.
func Run(ctx context.Context, r *root.Root, store Blob, trust Trust, a RunArgs) (string, error) {
	cmds, hash, err := PendingCommands(r)
	if err != nil {
		return "", err
	}
	c, ok := cmds[a.Name]
	if !nameRe.MatchString(a.Name) || !ok {
		return "", notAllowed(a.Name, cmds)
	}
	// The file comes from the repository, which may be hostile, so it only
	// counts once the user has approved this exact version of it.
	if trust == nil || !trust.Approved(r.Dir(), hash) {
		return "", errors.New(commandsFile + " has not been approved on this machine, so nothing was run. " +
			"Repositories can list any program there, so Trove asks you first. " +
			"Ask the user to review the file and run `trove mcp trust` in a terminal in the project folder; " +
			"approval ends if the file changes")
	}
	timeout := defaultTimeout
	if c.TimeoutSec > 0 {
		timeout = time.Duration(c.TimeoutSec) * time.Second
	}
	out, exit, dur, runErr := execute(ctx, r.Dir(), c.Run, timeout)

	var b strings.Builder
	switch {
	case errors.Is(runErr, context.DeadlineExceeded):
		fmt.Fprintf(&b, "%s: TIMEOUT after %ds (killed)\n", a.Name, int(timeout.Seconds()))
	case runErr != nil:
		return "", fmt.Errorf("could not start %q: %v. Check the command in %s", c.Run[0], runErr, commandsFile)
	case exit != 0:
		fmt.Fprintf(&b, "%s: FAIL (exit %d, %.1fs)\n", a.Name, exit, dur.Seconds())
	default:
		fmt.Fprintf(&b, "%s: PASS (%.1fs)\n", a.Name, dur.Seconds())
	}
	if names := failingNames(out); len(names) > 0 {
		fmt.Fprintf(&b, "failing: %s\n", strings.Join(names, ", "))
	}

	text := string(out)
	id, putErr := store.Put(out)
	if putErr != nil {
		// Fail open: without a stored copy nothing may be trimmed.
		body, _ := joinLimited(splitLines(text), maxInlineBytes*2)
		b.WriteString(body)
		b.WriteString("\n[full output could not be stored, so it was not shortened; ")
		b.WriteString("showing the start only]\n")
		return b.String(), nil
	}
	shown := text
	if cond, changed := optimize.Condense(text); changed {
		shown = cond
	} else if len(text) > maxInlineBytes {
		// Too uniform to condense by rules: show the head and tail.
		lines := splitLines(text)
		head, n := joinLimited(lines, maxInlineBytes/2)
		tailStart := max(n, len(lines)-40)
		shown = head + fmt.Sprintf("[Trove: %d lines trimmed; full output sha256:%s]\n", tailStart-n, id) + strings.Join(lines[tailStart:], "")
	}
	b.WriteString(shown)
	if !strings.HasSuffix(shown, "\n") {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "output_id: %s (%d lines; trove_fetch_full returns all of it)\n", id, strings.Count(text, "\n")+1)
	return b.String(), nil
}

func notAllowed(name string, cmds map[string]Command) error {
	if len(cmds) == 0 {
		return fmt.Errorf("no commands are allowed in this project. Create %s with a [commands.NAME] table; see docs/mcp.md", commandsFile)
	}
	names := make([]string, 0, len(cmds))
	for n := range cmds {
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Errorf("%q is not an allowed command. Allowed: %s", name, strings.Join(names, ", "))
}

// failingNames lists test or check names that failed, in order and without
// repeats, from the formats the common runners print.
func failingNames(out []byte) []string {
	seen := map[string]bool{}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		m := failNamed.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		n := ""
		for _, g := range m[1:] {
			if g != "" {
				n = strings.TrimSpace(g)
				break
			}
		}
		if n == "" || seen[n] || n == "FAIL" {
			continue
		}
		seen[n] = true
		if len(names) == maxFailingListed {
			names = append(names, "…")
			break
		}
		names = append(names, n)
	}
	return names
}

type capWriter struct {
	buf bytes.Buffer
	cut bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := maxCaptureBytes - w.buf.Len(); room < len(p) {
		w.buf.Write(p[:max(room, 0)])
		w.cut = true
	} else {
		w.buf.Write(p)
	}
	return len(p), nil
}

// execute runs argv in dir with a filtered environment and returns combined
// output. exit is -1 when the program could not be started.
func execute(ctx context.Context, dir string, argv []string, timeout time.Duration) (out []byte, exit int, dur time.Duration, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv comes from the commands.toml the user approved with trove mcp trust; the model only picks a name
	cmd.Dir = dir
	cmd.Env = filteredEnv(os.Environ())
	w := &capWriter{}
	cmd.Stdout, cmd.Stderr = w, w
	cmd.WaitDelay = 2 * time.Second
	setProcessGroup(cmd)
	start := time.Now()
	runErr := cmd.Run()
	dur = time.Since(start)
	out = w.buf.Bytes()
	if w.cut {
		out = append(out, []byte("\n[output cut at 8 MB]\n")...)
	}
	if ctx.Err() != nil {
		return out, -1, dur, ctx.Err()
	}
	var ee *exec.ExitError
	switch {
	case runErr == nil:
		return out, 0, dur, nil
	case errors.As(runErr, &ee):
		return out, ee.ExitCode(), dur, nil
	}
	return out, -1, dur, runErr
}

// filteredEnv drops variables that look like credentials, so a project command
// never inherits the agent's API keys.
func filteredEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if envDropRe.MatchString(k) {
			continue
		}
		out = append(out, kv)
	}
	return out
}
