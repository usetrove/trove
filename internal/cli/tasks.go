package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/fixer"
	"github.com/usetrove/trove/internal/tasks"
)

type tasksFlags struct {
	project, testCmd, scoutModel, workerModel, escalationModel string
	maxTurns                                                   int
	yes                                                        bool
}

func newTasks(a *App) *cobra.Command {
	var fl tasksFlags
	c := &cobra.Command{
		Use:   "tasks",
		Short: "Add a read-only scout subagent or a test-gated cascade to a project, with approval and undo",
		Long: "Two opt-in delegation features for Claude Code projects. Nothing is changed by this command alone.\n\n" +
			"  trove tasks plan  scout     show the scout subagent (small model, read-only tools) and its CLAUDE.md note\n" +
			"  trove tasks plan  cascade   show /trove-cascade: cheap model first, strong model only if your tests fail\n" +
			"  trove tasks apply scout|cascade   write the files (asked first; backed up; `trove undo` reverses it)\n\n" +
			"Files are written to the project's .claude folder, between sessions only. Trove never overwrites an agent or command\n" +
			"file you wrote. The cascade needs your test command (--test-cmd, or test_cmd under [tasks] in config.toml) and tells you\n" +
			"which model finished and why. Savings are not claimed: `trove report` compares your sessions once enough exist.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	feature := cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs)
	flags := func(sub *cobra.Command) {
		sub.Flags().StringVar(&fl.project, "project", "", "project folder (default: the current folder)")
		sub.Flags().StringVar(&fl.testCmd, "test-cmd", "", "the project's test command (cascade only)")
		sub.Flags().StringVar(&fl.scoutModel, "scout-model", "", "model for the scout (default haiku)")
		sub.Flags().StringVar(&fl.workerModel, "worker-model", "", "cheap model for the first attempt (default haiku)")
		sub.Flags().StringVar(&fl.escalationModel, "escalation-model", "", "strong model for the retry (default opus)")
		sub.Flags().IntVar(&fl.maxTurns, "max-turns", 0, "turn limit for the cheap worker (default 25)")
	}
	plan := &cobra.Command{Use: "plan scout|cascade", Short: "Show what would be written; changes nothing", Args: feature, ValidArgs: []string{"scout", "cascade"},
		RunE: func(_ *cobra.Command, args []string) error { return runTasks(a, fl, args[0], false) }}
	apply := &cobra.Command{Use: "apply scout|cascade", Short: "Write the files after showing the diff and asking", Args: feature, ValidArgs: []string{"scout", "cascade"},
		RunE: func(_ *cobra.Command, args []string) error { return runTasks(a, fl, args[0], true) }}
	apply.Flags().BoolVar(&fl.yes, "yes", false, "do not ask; you have reviewed what will change")
	flags(plan)
	flags(apply)
	c.AddCommand(plan, apply)
	return c
}

// tasksOptions merges config values with flags; flags win.
func tasksOptions(cfg config.Tasks, fl tasksFlags) tasks.Options {
	pick := func(flag, cfgv string) string {
		if flag != "" {
			return flag
		}
		return cfgv
	}
	o := tasks.Options{
		ScoutModel: pick(fl.scoutModel, cfg.ScoutModel), WorkerModel: pick(fl.workerModel, cfg.WorkerModel),
		EscalationModel: pick(fl.escalationModel, cfg.EscalationModel), TestCmd: pick(fl.testCmd, cfg.TestCmd), MaxTurns: cfg.MaxTurns,
	}
	if fl.maxTurns != 0 {
		o.MaxTurns = fl.maxTurns
	}
	return o
}

type tasksReport struct {
	Project  string         `json:"project"`
	Feature  string         `json:"feature"`
	Active   bool           `json:"agent_active"`
	Reason   string         `json:"agent_active_reason,omitempty"`
	Overhead tasks.Overhead `json:"overhead_estimated"`
	Files    []tasks.File   `json:"files"`
	Fixes    []*fixer.Fix   `json:"fixes"`
	Errors   []string       `json:"errors,omitempty"`
}

func runTasks(a *App, fl tasksFlags, which string, apply bool) error {
	if fl.yes && !apply {
		return errors.New("--yes only makes sense with `apply`. Nothing was changed")
	}
	feat := map[string]string{"scout": tasks.FeatureScout, "cascade": tasks.FeatureCascade}[which]
	if feat == "" {
		return fmt.Errorf("unknown feature %q; use scout or cascade", which)
	}
	e, closeDB, err := fixEnv(a, fl.project)
	if err != nil {
		return err
	}
	defer closeDB()
	_, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	e.Tasks = &fixer.TasksRequest{Feature: feat, Options: tasksOptions(cfg.Tasks, fl)}
	ctx := a.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	files, err := fixer.PlanTasks(e)
	if err != nil {
		return err
	}
	all, errs := fixer.Detect(ctx, e)
	act := fixer.CheckActivity(ctx, e)
	rep := tasksReport{Project: e.ProjectDir, Feature: feat, Active: act.Active, Reason: act.Reason, Overhead: tasks.OverheadOf(files), Files: files}
	for _, f := range all {
		if f.Kind == feat {
			rep.Fixes = append(rep.Fixes, f)
		}
	}
	for _, err := range errs {
		rep.Errors = append(rep.Errors, err.Error())
	}
	if !apply {
		return a.emit(rep, renderTasks(rep))
	}

	var chosen []string
	var refused []string
	for _, f := range rep.Fixes {
		if f.Applicable() {
			chosen = append(chosen, f.ID)
		} else {
			refused = append(refused, f.Title+": "+firstNonEmpty(f.Blocked, f.Advisory))
		}
	}
	if len(chosen) == 0 {
		msg := "Nothing to apply for the " + which + "."
		if len(refused) > 0 {
			msg += "\n" + strings.Join(refused, "\n")
		} else {
			msg += " It is already in place."
		}
		return a.emit(fixApplyReport{Results: []fixer.Result{}}, msg)
	}
	if !a.JSON {
		_, _ = fmt.Fprint(a.Out, renderTasks(rep))
	}
	consent := fl.yes
	if !consent && !a.JSON {
		q := fmt.Sprintf("Write %d file(s) for the %s? Originals are backed up and `trove undo` reverses it.", len(chosen), which)
		if act.Active {
			q = "An agent session looks active, so this will be queued, not applied. Queue it?"
		}
		consent = a.confirm(q)
	}
	if !consent {
		return a.emit(fixApplyReport{Results: []fixer.Result{}}, "No changes made. Re-run with --yes (or answer y) to apply.")
	}
	res, err := fixer.Apply(ctx, e, chosen)
	out := summarize(res)
	if err != nil {
		_ = a.emit(out, "")
		return err
	}
	tail := "\nStart a new Claude Code session to use it."
	if feat == tasks.FeatureCascade {
		tail += " Run it as /" + tasks.CascadeName + " <task>."
	}
	return a.emit(out, renderResults(res)+tail)
}

func renderTasks(r tasksReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s for %s\n", map[string]string{tasks.FeatureScout: "Scout subagent", tasks.FeatureCascade: "Cascade workflow"}[r.Feature], r.Project)
	b.WriteString(taskWhyText(r.Feature) + "\n")
	fmt.Fprintf(&b, "Overhead (estimated): about %d tokens in every request, about %d more each time it is used. Trove charges this against any saving.\n",
		r.Overhead.PerRequestTokens, r.Overhead.PerUseTokens)
	if len(r.Fixes) == 0 && len(r.Errors) == 0 {
		b.WriteString("\nAlready in place: nothing to change.\n")
	}
	for _, f := range r.Fixes {
		b.WriteString("\n" + renderFixDetail(f, fixer.Activity{}))
	}
	if r.Active {
		b.WriteString("\n" + r.Reason + " Applying will queue the change.\n")
	}
	for _, e := range r.Errors {
		b.WriteString("\nCould not plan: " + e + "\n")
	}
	b.WriteString("\nNothing was changed. Apply with `trove tasks apply " + strings.TrimPrefix(r.Feature, "task_") + "`.\n")
	return b.String()
}

func taskWhyText(feature string) string {
	if feature == tasks.FeatureScout {
		return "Exploration moves into a small-model, read-only subagent that returns a short summary."
	}
	return "A task starts on a cheaper model; the strong model is used only if your tests fail, and you are told which one finished."
}
