package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/hooks"
	hookprobe "github.com/usetrove/trove/internal/hooks/probe"
	"github.com/usetrove/trove/internal/report"
	"github.com/usetrove/trove/internal/setup"
)

// The Claude Code plugin (plugin/trove, see docs/plugin.md) only wires Claude Code
// to this binary. These commands are what its SessionStart hook and slash
// commands call. Nothing here reads or writes credentials, and nothing is turned
// on: the first-run report is read-only and every optimizer stays off until the
// user enables it.

// pluginStateFile records what the plugin has already done on this machine.
const pluginStateFile = "plugin_state.json"

type pluginState struct {
	FirstRun      string `json:"first_run,omitempty"`      // RFC 3339; set once the first-run report was produced
	ClaudeVersion string `json:"claude_version,omitempty"` // Claude Code version the capability record was last checked against
}

func loadPluginState(dir string) pluginState {
	var s pluginState
	if b, err := os.ReadFile(filepath.Join(dir, pluginStateFile)); err == nil { //nolint:gosec // path under Trove's data dir
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func savePluginState(dir string, s pluginState) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, pluginStateFile), append(b, '\n'), 0o600)
}

func newPlugin(a *App) *cobra.Command {
	c := &cobra.Command{
		Use:   "plugin",
		Short: "Commands the Claude Code plugin calls (first-run report, capability check, status line)",
		Long: "The Trove plugin for Claude Code only wires Claude Code to this program. These commands do the work behind it.\n" +
			"None of them turns an optimizer on. See docs/plugin.md.",
	}
	var yes bool
	sl := &cobra.Command{
		Use:   "status-line",
		Short: "Add Trove to Claude Code's status line (shows the change first; asks before applying)",
		Long: "Plugins cannot set a status line, so this is a separate step. It shows the exact change, backs up settings.json,\n" +
			"and applies only with --yes (or a yes at the prompt). `trove undo` or `trove uninstall` removes it. An existing status line is never replaced.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runPluginStatusLine(a, yes) },
	}
	sl.Flags().BoolVar(&yes, "yes", false, "apply the change shown without asking")
	c.AddCommand(
		&cobra.Command{
			Use:   "start",
			Short: "Run at session start: first-run report and capability check (used by the plugin's SessionStart hook)",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return runPluginStart(a) },
		},
		sl,
		&cobra.Command{
			Use:   "status",
			Short: "Show whether the plugin's hooks are active and what the installed Claude Code version allows",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return runPluginStatus(a) },
		},
	)
	return c
}

// hookOutput is the JSON a SessionStart hook may print. systemMessage is the
// documented "warning message shown to the user" field (code.claude.com/docs/en/hooks,
// read 2026-11-05); whether it is displayed for SessionStart is not stated in the
// docs, which is why /trove:report always works as well.
type hookOutput struct {
	SystemMessage string `json:"systemMessage"`
}

// runPluginStart never fails: a SessionStart hook that errors must not trouble the
// session (rule 4). Problems are swallowed and the next session tries again.
func runPluginStart(a *App) error {
	a.Err = io.Discard
	a.NoColor = true
	defer func() { _ = recover() }()
	dir, _, err := loadConfig(a)
	if err != nil {
		return nil
	}
	st := loadPluginState(dir)
	var msgs []string

	if m := pluginCapabilityCheck(a, dir, &st); m != "" {
		msgs = append(msgs, m)
	}
	if hs, _ := setup.HooksInstalled(dir); len(hs) > 0 && a.Getenv("CLAUDE_PLUGIN_ROOT") != "" {
		msgs = append(msgs, "Trove's hooks are registered twice: once by the plugin and once in "+hs[0].Path+". Run `trove hooks uninstall` so each runs once; the plugin's stay.")
	}
	if st.FirstRun == "" {
		if text, err := pluginFirstRunReport(a); err == nil {
			msgs = append(msgs, text)
			st.FirstRun = time.Now().UTC().Format(time.RFC3339)
		}
	}
	_ = savePluginState(dir, st)
	if len(msgs) == 0 {
		return nil
	}
	b, err := json.Marshal(hookOutput{SystemMessage: strings.Join(msgs, "\n\n")})
	if err != nil {
		return nil
	}
	_, _ = fmt.Fprintln(a.Out, string(b))
	return nil
}

// pluginCapabilityCheck compares the installed Claude Code version with the one the
// hook capability record was made for. When it differs, an update may have changed
// what hooks can rewrite, so the record is replaced by a documentation-only probe
// (nothing is run, nothing costs money), which marks every rewrite unsupported. The
// rewriting optimizers then stay in shadow until the user approves a live probe.
func pluginCapabilityCheck(a *App, dir string, st *pluginState) string {
	bin, err := a.LookPath("claude")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ver, err := hookprobe.Version(ctx, hookprobe.Options{Claude: bin})
	if err != nil || ver == "" || ver == st.ClaudeVersion {
		return ""
	}
	old := st.ClaudeVersion
	rep, err := hookprobe.Run(ctx, hookprobe.Options{Claude: bin})
	if err != nil {
		return ""
	}
	hadLive := false
	for _, e := range hooks.LoadCapabilities(dir).Entries {
		if e.Agent == "claude" && e.Method == "live" {
			hadLive = true
		}
	}
	if err := hooks.SaveCapabilities(dir, mergeCapabilities(hooks.LoadCapabilities(dir), rep.Capabilities)); err != nil {
		return ""
	}
	st.ClaudeVersion = ver
	if old != "" && hadLive {
		return fmt.Sprintf("Claude Code changed from %s to %s, so Trove put its hook rewriting back in shadow mode (it only counts, changes nothing) until it is re-tested. Run /trove:setup to re-test.", old, ver)
	}
	return ""
}

// pluginFirstRunReport reads the agent's own history (read-only) and renders the
// headline report. It enables nothing.
func pluginFirstRunReport(a *App) (string, error) {
	r, err := buildReport(a, periodFlags{})
	if err != nil {
		return "", err
	}
	if r.State == report.StateEmpty || r.State == report.StateQuiet {
		// The report's own advice here is about the gateway (`trove setup`, `trove
		// gateway`), which is wrong for a plugin user, who may be on a subscription.
		r.Notes = []string{
			"Trove found no Claude Code session history to report on yet. It reads your sessions' own log files, read-only, and stores only counts, sizes and model names.",
			"Use Claude Code as usual, then run /trove:report. Your first numbers appear after a few sessions.",
		}
	}
	text := strings.TrimRight(report.Render(r, report.Options{Width: 100}), "\n")
	return "Trove is installed. Nothing is turned on and nothing about your sessions has changed.\n\n" + text +
		"\n\nEvery number above is labeled measured or estimated. Run /trove:setup to choose what to enable; each option starts in shadow mode (estimate only). /trove:report shows this again.", nil
}

func runPluginStatusLine(a *App, yes bool) error {
	dir, _, err := loadConfig(a)
	if err != nil {
		return err
	}
	home, err := a.HomeDir()
	if err != nil {
		return errors.New("cannot find your home directory; set HOME and try again")
	}
	rep := setupReport{}
	text, err := statusLineStepRetry(a, &rep, dir, supportedAgents[0].configDir(a, home), yes, "trove plugin status-line --yes")
	if err != nil {
		return err
	}
	return a.emit(rep, strings.TrimRight(text, "\n"))
}

type pluginStatus struct {
	DataDir        string          `json:"data_dir"`
	InPlugin       bool            `json:"running_from_plugin"`
	FirstRunDone   bool            `json:"first_run_done"`
	ClaudeVersion  string          `json:"claude_version,omitempty"`
	CheckedVersion string          `json:"capabilities_checked_for,omitempty"`
	Rewrites       map[string]bool `json:"hook_rewrites_supported"` // "Bash input", ...; true only after a live probe
	DuplicateHooks bool            `json:"hooks_also_in_settings"`
	Next           []string        `json:"next,omitempty"`
}

func runPluginStatus(a *App) error {
	dir, _, err := loadConfig(a)
	if err != nil {
		return err
	}
	st := loadPluginState(dir)
	caps := hooks.LoadCapabilities(dir)
	s := pluginStatus{DataDir: dir, InPlugin: a.Getenv("CLAUDE_PLUGIN_ROOT") != "", FirstRunDone: st.FirstRun != "",
		CheckedVersion: st.ClaudeVersion, Rewrites: map[string]bool{}, ClaudeVersion: installedClaudeVersion(a)}
	for _, t := range hookprobe.DefaultTools {
		s.Rewrites[t+" input"] = caps.Supports("claude", t, hooks.RewriteInput)
		s.Rewrites[t+" output"] = caps.Supports("claude", t, hooks.RewriteOutput)
	}
	hs, _ := setup.HooksInstalled(dir)
	s.DuplicateHooks = len(hs) > 0
	if s.DuplicateHooks {
		s.Next = append(s.Next, "Hooks are also in your settings file; run `trove hooks uninstall` so the plugin's hooks are the only ones.")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Data folder: %s\nClaude Code: %s (capability record made for %s)\nFirst-run report: %v\n", dir, orDash(s.ClaudeVersion), orDash(s.CheckedVersion), s.FirstRunDone)
	b.WriteString("Hook rewriting seen to work (needs a live test; until then these optimizers only count):\n")
	for _, t := range hookprobe.DefaultTools {
		fmt.Fprintf(&b, "  %s: input %v, output %v\n", t, s.Rewrites[t+" input"], s.Rewrites[t+" output"])
	}
	for _, n := range s.Next {
		b.WriteString(n + "\n")
	}
	return a.emit(s, strings.TrimRight(b.String(), "\n"))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// pluginHooksActive reports whether the Trove plugin is enabled where it would
// affect the install being planned: in the user's settings, or in the project's
// committed or local settings for --scope project.
func pluginHooksActive(claudeDir string, fl hooksInstallFlags) bool {
	if pluginEnabled(filepath.Join(claudeDir, "settings.json")) {
		return true
	}
	if fl.scope != "project" {
		return false
	}
	proj := fl.projectDir
	if proj == "" {
		var err error
		if proj, err = os.Getwd(); err != nil {
			return false
		}
	}
	return pluginEnabled(filepath.Join(proj, ".claude", "settings.json")) || pluginEnabled(filepath.Join(proj, ".claude", "settings.local.json"))
}

// pluginEnabled reports whether the Trove plugin is enabled in the settings file
// at path (enabledPlugins has a "trove@<marketplace>" key set to true). A missing
// or unreadable file is "no".
func pluginEnabled(path string) bool {
	b, err := os.ReadFile(path) //nolint:gosec // the user's own Claude Code settings, read-only
	if err != nil {
		return false
	}
	var doc struct {
		Enabled map[string]json.RawMessage `json:"enabledPlugins"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return false
	}
	for k, v := range doc.Enabled {
		if strings.HasPrefix(k, "trove@") && strings.TrimSpace(string(v)) == "true" {
			return true
		}
	}
	return false
}
