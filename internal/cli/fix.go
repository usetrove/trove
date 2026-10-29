package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/fixer"
	"github.com/usetrove/trove/internal/pricing"
)

type fixFlags struct {
	apply, yes, all, applyQueued bool
	project                      string
}

func newFix(a *App) *cobra.Command {
	var fl fixFlags
	c := &cobra.Command{
		Use:   "fix [id]",
		Short: "Propose evidence-based fixes to your agent's config files; apply them only with your approval",
		Long: "With no id, lists the fixes available for this project with their estimated savings. Nothing is changed.\n" +
			"With an id, shows the fix and its exact diff. Add --apply to apply it (you are asked first; --yes skips the question).\n" +
			"--all --apply applies every fix except suggestions (a cheaper default model), after showing a summary and asking once.\n" +
			"Every changed file is backed up and `trove undo <id>` restores it exactly. Fixes are never applied while an agent session\n" +
			"may be active; they are queued, and `trove fix --apply-queued` applies them once the agent is idle.\n" +
			"Trove never edits managed or policy files, credential files, or anything outside the agent config files it knows.\n" +
			"All savings shown are estimates.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return runFix(a, fl, args) },
	}
	c.Flags().BoolVar(&fl.apply, "apply", false, "apply the fix (or --all) after asking")
	c.Flags().BoolVar(&fl.yes, "yes", false, "do not ask; you have reviewed what will change")
	c.Flags().BoolVar(&fl.all, "all", false, "with --apply: every applicable fix except suggestions")
	c.Flags().BoolVar(&fl.applyQueued, "apply-queued", false, "apply fixes queued earlier, if your agent is idle now")
	c.Flags().StringVar(&fl.project, "project", "", "project folder to check (default: the current folder)")
	return c
}

func fixEnv(a *App, project string) (*fixer.Env, func(), error) {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return nil, nil, err
	}
	home, err := a.HomeDir()
	if err != nil {
		return nil, nil, errors.New("cannot find your home directory; set HOME and try again")
	}
	if project == "" {
		if project, err = os.Getwd(); err != nil {
			return nil, nil, errors.New("cannot tell which folder you are in. Pass --project <folder>")
		}
	}
	if project, err = filepath.Abs(project); err != nil {
		return nil, nil, fmt.Errorf("cannot use --project: %v", err)
	}
	e := &fixer.Env{Home: home, ProjectDir: project, DataDir: dir, Getenv: a.Getenv, Cfg: cfg.Fixer}
	for _, spec := range supportedAgents {
		if spec.binary == "codex" {
			e.CodexDir = spec.configDir(a, home)
		} else {
			e.ClaudeDir = spec.configDir(a, home)
		}
	}
	if t, err := pricing.Default(); err == nil {
		e.Pricing = t
	}
	closer := func() {}
	db, err := openExisting(dir)
	if err != nil {
		return nil, nil, err
	}
	if db != nil {
		e.DB = db.DB
		closer = func() { _ = db.Close() }
	}
	return e, closer, nil
}

type fixListReport struct {
	Project string         `json:"project"`
	Active  bool           `json:"agent_active"`
	Reason  string         `json:"agent_active_reason,omitempty"`
	Fixes   []*fixer.Fix   `json:"fixes"`
	Queued  []fixer.Queued `json:"queued,omitempty"`
	Errors  []string       `json:"errors,omitempty"`
}

type fixApplyReport struct {
	Results []fixer.Result `json:"results"`
	Applied int            `json:"applied"`
	Queued  int            `json:"queued"`
}

func runFix(a *App, fl fixFlags, args []string) error {
	if fl.all && len(args) > 0 {
		return errors.New("give either an id or --all, not both. Run `trove fix` to see the ids")
	}
	if (fl.all || fl.yes) && !fl.apply {
		return errors.New("--all and --yes only make sense with --apply. Nothing was changed")
	}
	e, closeDB, err := fixEnv(a, fl.project)
	if err != nil {
		return err
	}
	defer closeDB()
	ctx := a.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	if fl.applyQueued {
		res, err := fixer.ApplyQueued(ctx, e)
		rep := summarize(res)
		if err != nil {
			_ = a.emit(rep, "")
			return err
		}
		if len(res) == 0 {
			return a.emit(rep, "Nothing is queued.")
		}
		return a.emit(rep, renderResults(res))
	}

	fixes, errs := fixer.Detect(ctx, e)
	act := fixer.CheckActivity(ctx, e)
	queued, _ := fixer.LoadQueue(e.DataDir)

	if len(args) == 0 && !fl.all {
		rep := fixListReport{Project: e.ProjectDir, Active: act.Active, Reason: act.Reason, Queued: queued}
		for _, f := range fixes {
			c := *f
			c.Diff = ""
			rep.Fixes = append(rep.Fixes, &c)
		}
		for _, err := range errs {
			rep.Errors = append(rep.Errors, err.Error())
		}
		return a.emit(rep, renderFixList(rep))
	}

	// Pick what to apply or show.
	var chosen []*fixer.Fix
	if fl.all {
		for _, f := range fixes {
			if f.Applicable() && !f.Suggestion {
				chosen = append(chosen, f)
			}
		}
		if len(chosen) == 0 {
			return a.emit(fixApplyReport{}, "No fixes to apply. Suggestions such as a cheaper default model are never part of --all; apply one by id.")
		}
	} else {
		f := fixer.Find(fixes, args[0])
		if f == nil {
			return fmt.Errorf("no fix has the id %q for %s. Run `trove fix` to see what is available", args[0], e.ProjectDir)
		}
		chosen = []*fixer.Fix{f}
	}

	if !fl.apply {
		if a.JSON {
			return a.emit(chosen[0], "")
		}
		_, _ = fmt.Fprint(a.Out, renderFixDetail(chosen[0], act))
		return nil
	}

	var blocked []string
	for _, f := range chosen {
		if !f.Applicable() {
			blocked = append(blocked, f.ID+": "+firstNonEmpty(f.Blocked, "Trove cannot make this change itself. "+f.Advisory))
		}
	}
	if len(blocked) > 0 && !fl.all {
		return errors.New(strings.Join(blocked, "\n") + "\nNothing was changed")
	}

	if !a.JSON {
		if fl.all {
			_, _ = fmt.Fprintf(a.Out, "%d fix(es) will be applied. Estimated savings, not measured:\n\n", len(chosen))
			for _, f := range chosen {
				_, _ = fmt.Fprintf(a.Out, "  %-12s %s\n      %s\n", f.ID, f.Title, relOrAbs(e.ProjectDir, f.Path))
			}
			_, _ = fmt.Fprintln(a.Out)
		} else {
			_, _ = fmt.Fprint(a.Out, renderFixDetail(chosen[0], act))
		}
	}
	consent := fl.yes
	if !consent && !a.JSON {
		q := fmt.Sprintf("Apply %d fix(es)? Originals are backed up and `trove undo` reverses them.", len(chosen))
		if act.Active {
			q = "An agent session looks active, so this will be queued, not applied. Queue it?"
		}
		consent = a.confirm(q)
	}
	if !consent {
		return a.emit(fixApplyReport{}, "No changes made. Re-run with --yes (or answer y) to apply.")
	}
	ids := make([]string, 0, len(chosen))
	for _, f := range chosen {
		ids = append(ids, f.ID)
	}
	res, err := fixer.Apply(ctx, e, ids)
	rep := summarize(res)
	if err != nil {
		_ = a.emit(rep, "")
		return err
	}
	return a.emit(rep, renderResults(res))
}

func summarize(res []fixer.Result) fixApplyReport {
	rep := fixApplyReport{Results: res}
	if rep.Results == nil {
		rep.Results = []fixer.Result{}
	}
	for _, r := range res {
		switch r.Status {
		case "applied":
			rep.Applied++
		case "queued":
			rep.Queued++
		}
	}
	return rep
}

func renderResults(res []fixer.Result) string {
	var b strings.Builder
	for _, r := range res {
		fmt.Fprintf(&b, "%s: %s. %s\n", r.ID, r.Status, r.Detail)
	}
	b.WriteString("\nTo reverse: `trove undo <id>` (one fix) or `trove undo --all`. Results appear in `trove report` after a few sessions, labeled measured only once there is enough data.")
	return b.String()
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func relOrAbs(base, p string) string {
	if rel, err := filepath.Rel(base, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return p
}

func renderFixList(r fixListReport) string {
	var b strings.Builder
	if len(r.Fixes) == 0 {
		b.WriteString("No fixes to suggest for " + r.Project + ".\n")
		b.WriteString("Trove only proposes a fix when it has evidence: enough recent sessions, or a file with something specific to remove.\n")
	} else {
		fmt.Fprintf(&b, "Fixes for %s (all savings are estimated):\n\n", r.Project)
		tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tKIND\tFILE\tEST. TOKENS/SESSION\tEST. $/SESSION\tSTATUS")
		for _, f := range r.Fixes {
			usd := "-"
			if f.USDPerSession != nil {
				usd = fmt.Sprintf("$%.4f", *f.USDPerSession)
			}
			tok := "-"
			if f.TokensPerSession > 0 {
				tok = fmt.Sprint(f.TokensPerSession)
			}
			status := "ready"
			switch {
			case f.Blocked != "":
				status = "blocked"
			case f.Advisory != "":
				status = "manual step"
			case f.Suggestion:
				status = "suggestion"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", f.ID, f.Kind, relOrAbs(r.Project, f.Path), tok, usd, status)
		}
		_ = tw.Flush()
		b.WriteString("\nSee one with `trove fix <id>`; apply it with `trove fix <id> --apply`. A dash means Trove has no honest estimate.\n")
	}
	if r.Active {
		b.WriteString("\n" + r.Reason + " Approved fixes will be queued until your agent is idle.\n")
	}
	if len(r.Queued) > 0 {
		fmt.Fprintf(&b, "\n%d fix(es) are queued. Run `trove fix --apply-queued` when your agent is idle.\n", len(r.Queued))
	}
	for _, e := range r.Errors {
		b.WriteString("\nCould not check one kind of fix: " + e + "\n")
	}
	return b.String()
}

func renderFixDetail(f *fixer.Fix, act fixer.Activity) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n%s\n\nWhy: %s\n", f.ID, f.Kind, f.Title, f.Why)
	if len(f.Evidence) > 0 {
		b.WriteString("\nEvidence:\n")
		for _, e := range f.Evidence {
			b.WriteString("  - " + e + "\n")
		}
	}
	if f.TokensPerSession > 0 {
		usd := "no dollar estimate"
		if f.USDPerSession != nil {
			usd = fmt.Sprintf("about $%.4f", *f.USDPerSession)
		}
		fmt.Fprintf(&b, "\nEstimated saving per session: about %d tokens, %s (estimated, not measured).\n", f.TokensPerSession, usd)
	}
	for _, n := range f.Notes {
		b.WriteString("Note: " + n + "\n")
	}
	switch {
	case f.Blocked != "":
		b.WriteString("\nCannot be applied: " + f.Blocked + "\n")
	case f.Advisory != "":
		b.WriteString("\nTrove cannot make this change itself. " + f.Advisory + "\n")
	case f.Diff != "":
		b.WriteString("\nChange to " + f.Path + ":\n\n" + f.Diff)
	}
	if act.Active && f.Applicable() {
		b.WriteString("\n" + act.Reason + " Applying will queue it.\n")
	}
	return b.String()
}
