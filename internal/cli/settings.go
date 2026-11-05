package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/fixer"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/settings"
)

// runVersionCmd runs `<program> --version`. Tests replace it so no real program
// is started. Nil means the real thing.
var runVersionCmd func(ctx context.Context, name string, args ...string) ([]byte, error)

type settingsFlags struct {
	task, project      string
	allowUnproven, yes bool
	allowUnprovenSet   bool
}

func newSettings(a *App) *cobra.Command {
	var fl settingsFlags
	c := &cobra.Command{
		Use:   "settings",
		Short: "Tune your agent's built-in token controls per task type, with approval, undo and measured results",
		Long: "Shows which token controls (thinking, compaction, output style, Codex profiles) Trove knows for the agents installed here,\n" +
			"checked against their installed versions. Nothing is changed by this command.\n\n" +
			"  trove settings plan  --task quick-fix    show exactly what a profile would change, and what it cannot\n" +
			"  trove settings apply --task quick-fix    apply it (asked first; backed up; `trove undo` reverses it)\n" +
			"  trove settings status                    what the data shows about profiles you applied\n\n" +
			"Profiles never lower a setting below the agent's default unless there is evidence it is safe; values without evidence\n" +
			"are left out unless you pass --allow-unproven. Trove never overrides managed (organization) settings, applies changes only\n" +
			"between sessions, and labels every result estimated until there are enough sessions to call it measured.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runSettingsList(a) },
	}
	flags := func(sub *cobra.Command, needTask bool) {
		if needTask {
			sub.Flags().StringVar(&fl.task, "task", "", "task type: "+strings.Join(taskNames(), ", ")+" (default: [settings] default_task)")
			sub.Flags().BoolVar(&fl.allowUnproven, "allow-unproven", false, "include values that have no benchmark or A/B evidence yet")
		}
		sub.Flags().StringVar(&fl.project, "project", "", "project folder (default: the current folder)")
	}
	plan := &cobra.Command{Use: "plan", Short: "Show what a profile would change; changes nothing", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fl.allowUnprovenSet = cmd.Flags().Changed("allow-unproven")
			return runSettingsPlan(a, fl, false)
		}}
	apply := &cobra.Command{Use: "apply", Short: "Apply a profile after showing the diff and asking", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fl.allowUnprovenSet = cmd.Flags().Changed("allow-unproven")
			return runSettingsPlan(a, fl, true)
		}}
	apply.Flags().BoolVar(&fl.yes, "yes", false, "do not ask; you have reviewed what will change")
	status := &cobra.Command{Use: "status", Short: "What the data shows about profiles you applied", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runSettingsStatus(a) }}
	flags(plan, true)
	flags(apply, true)
	c.AddCommand(plan, apply, status)
	return c
}

func taskNames() []string {
	c, err := settings.Builtin()
	if err != nil {
		return nil
	}
	p, err := settings.BuiltinProfiles(c)
	if err != nil {
		return nil
	}
	return p.Tasks()
}

// settingsEnv builds the fixer environment for a settings task.
func settingsEnv(a *App, fl settingsFlags) (*fixer.Env, func(), error) {
	e, closeDB, err := fixEnv(a, fl.project)
	if err != nil {
		return nil, nil, err
	}
	_, cfg, err := loadConfig(a)
	if err != nil {
		closeDB()
		return nil, nil, err
	}
	task := fl.task
	if task == "" {
		task = cfg.Settings.DefaultTask
	}
	if task == "" {
		closeDB()
		return nil, nil, fmt.Errorf("say which task type: --task %s (or set default_task under [settings] in config.toml)", strings.Join(taskNames(), " | "))
	}
	allow := cfg.Settings.AllowUnproven
	if fl.allowUnprovenSet {
		allow = fl.allowUnproven
	}
	e.Settings = &fixer.SettingsRequest{Task: task, AllowUnproven: allow}
	ctx := a.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	e.SettingsVersion = func(h string) string { return settings.InstalledVersion(ctx, h, runVersionCmd) }
	return e, closeDB, nil
}

// ---- list ------------------------------------------------------------------

type settingsListEntry struct {
	settings.Resolved
	Action string `json:"action"` // "applies", "suggestion only"
}

type settingsHarnessList struct {
	Harness     string              `json:"harness"`
	Installed   string              `json:"installed_version,omitempty"`
	DocsChecked string              `json:"docs_checked_on"`
	Settings    []settingsListEntry `json:"settings"`
	Gaps        []settings.Gap      `json:"gaps,omitempty"`
}

type settingsListReport struct {
	Tasks     []string              `json:"tasks"`
	Harnesses []settingsHarnessList `json:"harnesses"`
}

func runSettingsList(a *App) error {
	cat, err := settings.Builtin()
	if err != nil {
		return fmt.Errorf("the built-in settings list could not be loaded (%v). This is a bug in this Trove build; please report it", err)
	}
	ctx := a.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	rep := settingsListReport{Tasks: taskNames()}
	for _, f := range cat.Files {
		h := settingsHarnessList{Harness: f.Harness, DocsChecked: f.CheckedOn, Gaps: f.Gaps,
			Installed: settings.InstalledVersion(ctx, f.Harness, runVersionCmd)}
		for _, r := range f.Resolve(h.Installed) {
			act := "suggestion only"
			if r.Applyable {
				act = "applies"
			}
			h.Settings = append(h.Settings, settingsListEntry{Resolved: r, Action: act})
		}
		rep.Harnesses = append(rep.Harnesses, h)
	}
	return a.emit(rep, renderSettingsList(rep))
}

func renderSettingsList(r settingsListReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task types: %s\n", strings.Join(r.Tasks, ", "))
	for _, h := range r.Harnesses {
		ver := h.Installed
		if ver == "" {
			ver = "not found, or its version could not be read"
		}
		fmt.Fprintf(&b, "\n%s (installed: %s; documentation checked %s)\n", agentLabel(h.Harness), ver, h.DocsChecked)
		tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "SETTING\tWHERE\tDEFAULT\tRISK\tTROVE\tVERSION CHECK")
		for _, s := range h.Settings {
			def := firstNonEmpty(s.Default, s.DefaultNote, "-")
			if len(def) > 34 {
				def = def[:31] + "..."
			}
			chk := string(s.Status)
			if s.Reason != "" {
				chk += ": " + s.Reason
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Key, s.Target, def, s.Risk, s.Action, chk)
		}
		_ = tw.Flush()
		for _, g := range h.Gaps {
			fmt.Fprintf(&b, "Not offered: %s. %s\n", g.Key, g.Reason)
		}
	}
	b.WriteString("\n\"suggestion only\" settings are listed because the agent has them, but Trove has not confirmed them from the documentation well enough to write them.\n")
	b.WriteString("Next: `trove settings plan --task <type>` shows exactly what a profile would change.\n")
	return b.String()
}

func agentLabel(h string) string {
	if h == settings.Codex {
		return "Codex CLI"
	}
	return "Claude Code"
}

// ---- plan / apply ------------------------------------------------------------

type settingsPlanReport struct {
	Project string         `json:"project"`
	Active  bool           `json:"agent_active"`
	Reason  string         `json:"agent_active_reason,omitempty"`
	Plan    *settings.Plan `json:"plan"`
	Fixes   []*fixer.Fix   `json:"fixes"`
	Errors  []string       `json:"errors,omitempty"`
}

func runSettingsPlan(a *App, fl settingsFlags, apply bool) error {
	if fl.yes && !apply {
		return errors.New("--yes only makes sense with `apply`. Nothing was changed")
	}
	e, closeDB, err := settingsEnv(a, fl)
	if err != nil {
		return err
	}
	defer closeDB()
	ctx := a.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	plan, _, err := fixer.PlanSettings(e)
	if err != nil {
		return err
	}
	all, errs := fixer.Detect(ctx, e)
	act := fixer.CheckActivity(ctx, e)
	rep := settingsPlanReport{Project: e.ProjectDir, Active: act.Active, Reason: act.Reason, Plan: plan}
	for _, f := range all {
		if f.Kind == fixer.SettingsProfile {
			rep.Fixes = append(rep.Fixes, f)
		}
	}
	for _, err := range errs {
		rep.Errors = append(rep.Errors, err.Error())
	}

	if !apply {
		return a.emit(rep, renderSettingsPlan(rep))
	}

	var chosen []*fixer.Fix
	var refused []string
	for _, f := range rep.Fixes {
		if f.Applicable() {
			chosen = append(chosen, f)
		} else {
			refused = append(refused, agentLabel(f.Agent)+": "+firstNonEmpty(f.Blocked, "Trove cannot make this change itself. "+f.Advisory))
		}
	}
	if len(chosen) == 0 {
		msg := "Nothing to apply for the " + plan.Task + " profile."
		if len(refused) > 0 {
			msg += "\n" + strings.Join(refused, "\n")
		} else {
			msg += " Either it is already in place, or Trove has nothing it can write yet. `trove settings plan --task " + plan.Task + "` shows why."
		}
		return a.emit(fixApplyReport{Results: []fixer.Result{}}, msg)
	}
	if !a.JSON {
		_, _ = fmt.Fprint(a.Out, renderSettingsPlan(rep))
	}
	consent := fl.yes
	if !consent && !a.JSON {
		q := fmt.Sprintf("Apply the %q profile to %d file(s)? Originals are backed up and `trove undo` reverses it.", plan.Task, len(chosen))
		if act.Active {
			q = "An agent session looks active, so this will be queued, not applied. Queue it?"
		}
		consent = a.confirm(q)
	}
	if !consent {
		return a.emit(fixApplyReport{Results: []fixer.Result{}}, "No changes made. Re-run with --yes (or answer y) to apply.")
	}
	ids := make([]string, 0, len(chosen))
	for _, f := range chosen {
		ids = append(ids, f.ID)
	}
	res, err := fixer.Apply(ctx, e, ids)
	out := summarize(res)
	if err != nil {
		_ = a.emit(out, "")
		return err
	}
	return a.emit(out, renderResults(res)+"\nSee `trove settings status` for what the data shows after a few sessions.")
}

func renderSettingsPlan(r settingsPlanReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Profile %q for %s\n%s\n", r.Plan.Task, r.Project, r.Plan.Description)
	byAgent := map[string]*fixer.Fix{}
	for _, f := range r.Fixes {
		byAgent[f.Agent] = f
	}
	for _, hp := range r.Plan.Harnesses {
		fmt.Fprintf(&b, "\n== %s", agentLabel(hp.Harness))
		if hp.Version != "" {
			b.WriteString(" " + hp.Version)
		}
		b.WriteString(" ==\n")
		if hp.Unavailable != "" {
			b.WriteString("Skipped: " + hp.Unavailable + ".\n")
			continue
		}
		if f := byAgent[hp.Harness]; f != nil {
			b.WriteString(renderFixDetail(f, fixer.Activity{}))
			continue
		}
		// No fix: say why instead of staying silent.
		switch {
		case len(hp.Changes) > 0:
			b.WriteString("Already in place: nothing to change.\n")
		default:
			b.WriteString("Nothing to change.\n")
		}
		for _, s := range hp.Skipped {
			fmt.Fprintf(&b, "Left out: %s. %s\n", s.Value.Key, s.Reason)
		}
		for _, bl := range hp.Blocked {
			fmt.Fprintf(&b, "Not changed: %s. %s\n", bl.Key, bl.Reason)
		}
		for _, n := range hp.Notes {
			b.WriteString("Note: " + n + "\n")
		}
	}
	if r.Active {
		b.WriteString("\n" + r.Reason + " Applying will queue the change.\n")
	}
	for _, e := range r.Errors {
		b.WriteString("\nCould not check one kind of fix: " + e + "\n")
	}
	b.WriteString("\nNothing was changed. Apply with `trove settings apply --task " + r.Plan.Task + "`.\n")
	return b.String()
}

// ---- status ------------------------------------------------------------------

type settingsStatusReport struct {
	Profiles []measure.SettingsEffect `json:"profiles"`
}

func runSettingsStatus(a *App) error {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	db, err := openExisting(dir)
	if err != nil {
		return err
	}
	rep := settingsStatusReport{Profiles: []measure.SettingsEffect{}}
	if db != nil {
		defer func() { _ = db.Close() }()
		ctx := a.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		fx, err := measure.SettingsEffects(ctx, db, cfg.Measure)
		if err != nil {
			return err
		}
		if fx != nil {
			rep.Profiles = fx
		}
	}
	if len(rep.Profiles) == 0 {
		return a.emit(rep, "No settings profile is in place. `trove settings plan --task <type>` shows what one would change.")
	}
	sort.SliceStable(rep.Profiles, func(i, j int) bool { return rep.Profiles[i].AppliedAt.After(rep.Profiles[j].AppliedAt) })
	var b strings.Builder
	for _, p := range rep.Profiles {
		fmt.Fprintf(&b, "%s (%s, %s), applied %s\n  %s\n", p.FixID, p.Task, p.Harness, p.AppliedAt.Local().Format("2006-01-02"), p.Text)
		if p.Recommend != "" {
			b.WriteString("  Trove suggests undoing it: " + p.Recommend + "\n")
		}
	}
	return a.emit(rep, strings.TrimRight(b.String(), "\n"))
}
