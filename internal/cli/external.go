package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/external"
	"github.com/usetrove/trove/internal/store"
)

func newExternal(a *App) *cobra.Command {
	c := &cobra.Command{
		Use:   "external",
		Short: "Install, evaluate and remove third-party token-saving tools (each needs your approval)",
		Long: "Trove can wrap third-party token-saving tools so they are measured the same way as Trove's own\n" +
			"optimizers: their overhead is charged, their own savings claim is shown next to what Trove measures,\n" +
			"and a tool that costs more than it saves is switched off. Nothing is installed without your approval.",
	}
	c.AddCommand(newExternalList(a), newExternalShow(a), newExternalInstall(a), newExternalRemove(a),
		newExternalToggle(a, true), newExternalToggle(a, false), newExternalStatus(a))
	return c
}

// externalEnv opens the store and builds the lifecycle environment.
func externalEnv(a *App) (external.Env, config.Config, func(), error) {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return external.Env{}, cfg, nil, err
	}
	home, err := a.HomeDir()
	if err != nil {
		return external.Env{}, cfg, nil, errors.New("cannot find your home directory; set HOME and try again")
	}
	ms, err := externalManifests()
	if err != nil {
		return external.Env{}, cfg, nil, err
	}
	db, err := store.Open(dir)
	if err != nil {
		return external.Env{}, cfg, nil, fmt.Errorf("cannot open Trove's database: %v. Run `trove doctor` for details", err)
	}
	claudeDir := filepath.Join(home, ".claude")
	if d := a.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		claudeDir = d
	}
	env := external.Env{
		DataDir: dir, ClaudeDir: claudeDir, Runner: external.ExecRunner{}, Store: db.ExternalTools(), Manifests: ms,
		BuiltinsOn: func() []string { return builtinsOn(cfg) },
	}
	return env, cfg, func() { _ = db.Close() }, nil
}

func builtinsOn(cfg config.Config) []string {
	var out []string
	for n, s := range cfg.Optimizers {
		if s == config.StateOn && !strings.HasPrefix(n, external.Prefix) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

type externalRow struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	License        string `json:"license"`
	LicenseClass   string `json:"license_class"`
	ActsAs         string `json:"acts_as"`
	Category       string `json:"category"`
	Installed      bool   `json:"installed"`
	State          string `json:"state"`
	OverheadTokens int    `json:"overhead_tokens_per_request"`
	Label          string `json:"overhead_label"`
	Claimed        string `json:"claimed_by_tool"`
	SupportsShadow bool   `json:"supports_shadow"`
}

func externalRows(ctx context.Context, env external.Env) ([]externalRow, error) {
	recs, err := env.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	have := map[string]store.ExternalTool{}
	for _, r := range recs {
		have[r.Name] = r
	}
	var rows []externalRow
	for _, m := range env.Manifests {
		r := externalRow{Name: m.Name, Version: m.Version, License: m.License, LicenseClass: string(external.ClassifyLicense(m.License)),
			ActsAs: string(m.ActsAs), Category: string(m.Category), State: "not installed", OverheadTokens: external.ChargeFor(m).PerRequest,
			Label: "estimated", Claimed: m.Claimed.Text, SupportsShadow: m.SupportsShadow}
		if t, ok := have[m.Name]; ok {
			r.Installed, r.State = true, t.State
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func newExternalList(a *App) *cobra.Command {
	return &cobra.Command{
		Use: "list", Short: "List the third-party tools Trove can wrap", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			env, _, closeDB, err := externalEnv(a)
			if err != nil {
				return err
			}
			defer closeDB()
			rows, err := externalRows(c.Context(), env)
			if err != nil {
				return err
			}
			var b strings.Builder
			if len(rows) == 0 {
				b.WriteString("No external tools are available in this version.")
			}
			for _, r := range rows {
				fmt.Fprintf(&b, "%-14s v%-8s %-12s %-14s %s\n", r.Name, r.Version, r.LicenseClass, r.ActsAs, r.State)
			}
			return a.emit(map[string]any{"tools": rows}, strings.TrimRight(b.String(), "\n"))
		},
	}
}

func newExternalShow(a *App) *cobra.Command {
	return &cobra.Command{
		Use: "show <name>", Short: "Show a tool's license, what it does and what it costs", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			env, _, closeDB, err := externalEnv(a)
			if err != nil {
				return err
			}
			defer closeDB()
			plan, err := env.PlanInstall(c.Context(), args[0])
			if err != nil {
				return err
			}
			return a.emit(plan, planText(plan))
		},
	}
}

func planText(p external.InstallPlan) string {
	m := p.Manifest
	var b strings.Builder
	fmt.Fprintf(&b, "%s v%s (%s)\n  Source:   %s (checked %s)\n  License:  %s\n", m.Name, m.Version, m.ActsAs, m.SourceURL, m.VerifiedOn, m.License)
	if p.License.Warning != "" {
		fmt.Fprintf(&b, "  %s\n", p.License.Warning)
	}
	for _, c := range m.Cautions {
		fmt.Fprintf(&b, "  Caution:  %s\n", c)
	}
	if len(p.InstallArgv) > 0 {
		fmt.Fprintf(&b, "  Install:  %s\n", strings.Join(p.InstallArgv, " "))
	}
	if len(p.MissingRequires) > 0 {
		fmt.Fprintf(&b, "  Missing:  %s (must be on your PATH first)\n", strings.Join(p.MissingRequires, ", "))
	}
	fmt.Fprintf(&b, "  Overhead: %d tokens on every request while on (estimated; the cost of sessions with it on is compared with sessions with it off, and it is switched off if it costs more)\n", p.OverheadTokens)
	fmt.Fprintf(&b, "  Claims:   %s (the tool's own claim, %s)\n", m.Claimed.Text, m.Claimed.SourceURL)
	if p.ShadowNote != "" {
		fmt.Fprintf(&b, "  Shadow:   %s\n", p.ShadowNote)
	}
	return strings.TrimRight(b.String(), "\n")
}

// needTerminal enforces that a person is present: installing and removing run
// programs on the user's machine, so there is deliberately no --yes.
func needTerminal(what string) error {
	if !stdinIsTerminal() {
		return fmt.Errorf("%s needs a person at a terminal, so nothing was done. Run this command yourself in a terminal", what)
	}
	return nil
}

func (a *App) promptLine(question string) string {
	if a.In == nil {
		return ""
	}
	_, _ = fmt.Fprint(a.Out, question+" ")
	if a.inReader == nil || a.inSource != a.In {
		a.inReader, a.inSource = bufio.NewReader(a.In), a.In
	}
	line, _ := a.inReader.ReadString('\n')
	return strings.TrimSpace(line)
}

func newExternalInstall(a *App) *cobra.Command {
	return &cobra.Command{
		Use: "install <name>", Short: "Install a tool after showing its license and the exact command", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			env, _, closeDB, err := externalEnv(a)
			if err != nil {
				return err
			}
			defer closeDB()
			name := args[0]
			plan, err := env.PlanInstall(c.Context(), name)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(a.Out, planText(plan)+"\n")
			if err := needTerminal("installing " + name); err != nil {
				return err
			}
			ap := external.Approval{}
			if !a.confirm("Run the install command above and record " + name + " as installed?") {
				return a.emit(map[string]any{"installed": false}, "Not installed. Nothing changed.")
			}
			ap.Tool = name
			if plan.License.NeedsTypedConfirm {
				ap.Typed = a.promptLine("Type " + name + " to confirm you have read the license:")
			}
			if err := env.Install(c.Context(), name, ap); err != nil {
				return err
			}
			return a.emit(map[string]any{"installed": true, "name": name, "state": "off"},
				name+" is installed but off. Run `trove external enable "+name+"` to switch it on for new sessions.")
		},
	}
}

func newExternalRemove(a *App) *cobra.Command {
	return &cobra.Command{
		Use: "remove <name>", Short: "Switch a tool off, undo its config changes and uninstall it", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			env, _, closeDB, err := externalEnv(a)
			if err != nil {
				return err
			}
			defer closeDB()
			name := args[0]
			m, err := env.Find(name)
			if err != nil {
				return err
			}
			if len(m.Remove.Argv) > 0 {
				_, _ = fmt.Fprintf(a.Out, "This will undo %s's config changes, then run: %s\n", name, strings.Join(m.Remove.Argv, " "))
			} else {
				_, _ = fmt.Fprintf(a.Out, "This will undo %s's config changes. It has no uninstall command.\n", name)
			}
			if err := needTerminal("removing " + name); err != nil {
				return err
			}
			if !a.confirm("Remove " + name + "?") {
				return a.emit(map[string]any{"removed": false}, "Not removed. Nothing changed.")
			}
			if err := env.Remove(c.Context(), name, external.Approval{Tool: name}); err != nil {
				return err
			}
			return a.emit(map[string]any{"removed": true, "name": name}, name+" is removed and its config changes are undone.")
		},
	}
}

func newExternalToggle(a *App, enable bool) *cobra.Command {
	use, short := "disable <name>", "Switch a tool off for new sessions (it stays installed)"
	if enable {
		use, short = "enable <name>", "Switch a tool on for new sessions"
	}
	var yes bool
	c := &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			env, _, closeDB, err := externalEnv(a)
			if err != nil {
				return err
			}
			defer closeDB()
			name := args[0]
			ctx := c.Context()
			const restart = "Running agent sessions keep their current setup; the change applies to sessions you start afterwards."
			if !enable {
				res, err := env.Disable(ctx, name)
				if err != nil {
					return err
				}
				return a.emit(map[string]any{"name": name, "state": "off", "undone": res}, name+" is off. "+restart)
			}
			plan, err := env.PlanEnable(ctx, name)
			if err != nil {
				return err
			}
			if len(plan.Conflicts) > 0 {
				return plan.Conflicts[0]
			}
			var show strings.Builder
			for _, argv := range plan.Commands {
				fmt.Fprintf(&show, "Run: %s\n  Registers the tool with your agent (the agent keeps that setting in its own file, which Trove does not edit).\n", strings.Join(argv, " "))
			}
			for _, e := range plan.Edits {
				if e.NoOp {
					continue
				}
				fmt.Fprintf(&show, "%s\n  %s\n%s\n", e.Path, e.Summary, e.Show)
			}
			if show.Len() > 0 {
				_, _ = fmt.Fprintln(a.Out, show.String())
			}
			if !yes && !a.confirm("Switch "+name+" on?") {
				return a.emit(map[string]any{"enabled": false}, "Not enabled. No changes made. Re-run with --yes (or answer y) to apply.")
			}
			changes, err := env.Enable(ctx, name)
			if err != nil {
				return err
			}
			return a.emit(map[string]any{"name": name, "state": "on", "changes": len(changes)}, name+" is on. "+restart)
		},
	}
	if enable {
		c.Flags().BoolVar(&yes, "yes", false, "apply the config changes shown without asking")
	}
	return c
}

func newExternalStatus(a *App) *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Show installed tools, their state and any configuration problems", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			env, cfg, closeDB, err := externalEnv(a)
			if err != nil {
				return err
			}
			defer closeDB()
			rows, err := externalRows(c.Context(), env)
			if err != nil {
				return err
			}
			var problems []string
			for _, e := range external.ValidateStates(cfg.Optimizers, env.Manifests, builtinsOn(cfg)) {
				problems = append(problems, e.Error())
			}
			var b strings.Builder
			n := 0
			for _, r := range rows {
				if r.Installed {
					n++
					fmt.Fprintf(&b, "%-14s %-4s overhead %d tokens/request (estimated)\n", r.Name, r.State, r.OverheadTokens)
				}
			}
			if n == 0 {
				b.WriteString("No external tools are installed.\n")
			}
			for _, p := range problems {
				fmt.Fprintf(&b, "Problem: %s\n", p)
			}
			return a.emit(map[string]any{"tools": rows, "problems": problems}, strings.TrimRight(b.String(), "\n"))
		},
	}
}

// externalManifests is a variable so tests can supply a tool without
// shipping one in the binary.
var externalManifests = external.Builtin
