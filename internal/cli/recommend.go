package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/features"
	"github.com/usetrove/trove/internal/recommend"
	"github.com/usetrove/trove/internal/report"
	"github.com/usetrove/trove/internal/settings"
	"github.com/usetrove/trove/internal/store"
)

type recommendOut struct {
	Repo     string `json:"repo,omitempty"` // a hash, never the folder
	Task     string `json:"task,omitempty"`
	Defaults struct {
		Version int    `json:"version"`
		Release string `json:"release"`
	} `json:"defaults"`
	Recommendations []report.Recommendation `json:"recommendations"`
	Note            string                  `json:"note"`
}

func newRecommend(a *App) *cobra.Command {
	var repo, task string
	cmd := &cobra.Command{
		Use:   "recommend",
		Short: "Suggest optimizations and settings for this repo, with reasons",
		Long: "Looks at features Trove computed on this device (sizes, shares and counts; never the content), your own A/B\n" +
			"results and the defaults bundled with this release, and suggests which optimizers to try and which settings\n" +
			"profile fits the task. Each suggestion has a reason and a confidence level. Suggestions only: nothing is changed.\n" +
			"Optimizers are suggested in shadow mode first; \"turn on\" is only said when your own A/B result supports it.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return runRecommend(c.Context(), a, repo, task) },
	}
	cmd.Flags().StringVar(&repo, "repo", "", "the repo's folder (default: the current folder)")
	cmd.Flags().StringVar(&task, "task", "", "the task type: quick-fix, feature, refactor or exploration")
	return cmd
}

// repoKeyFor works out the repo key for a folder without creating anything: with
// no database or no salt yet there is no repo data either, and "" is returned.
func repoKeyFor(ctx context.Context, db *store.DB, repo string) string {
	if db == nil {
		return ""
	}
	if repo == "" {
		var err error
		if repo, err = os.Getwd(); err != nil {
			return ""
		}
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return ""
	}
	salt, ok, err := db.Features().ExistingSalt(ctx)
	if err != nil || !ok {
		return ""
	}
	return features.RepoKey(abs, salt)
}

func validTask(task string) error {
	if task == "" {
		return nil
	}
	cat, err := settings.Builtin()
	if err == nil {
		var ps *settings.ProfileSet
		if ps, err = settings.BuiltinProfiles(cat); err == nil {
			tasks := ps.Tasks()
			for _, t := range tasks {
				if t == task {
					return nil
				}
			}
			sort.Strings(tasks)
			return fmt.Errorf("%q is not a task type Trove knows. Use one of: %s", task, strings.Join(tasks, ", "))
		}
	}
	return fmt.Errorf("cannot read the settings profiles: %v", err)
}

func runRecommend(ctx context.Context, a *App, repo, task string) error {
	if err := validTask(task); err != nil {
		return err
	}
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	db, err := openExisting(dir)
	if err != nil {
		return err
	}
	out := recommendOut{Task: task, Recommendations: []report.Recommendation{}}
	d := recommend.BundledDefaults()
	out.Defaults.Version, out.Defaults.Release = d.Version, d.Release
	var recs []recommend.Recommendation
	if db != nil {
		defer func() { _ = db.Close() }()
		out.Repo = repoKeyFor(ctx, db, repo)
		facts, err := recommend.Gather(ctx, db, cfg, out.Repo, task, time.Now())
		if err != nil {
			return fmt.Errorf("could not read your data: %v. Run `trove doctor` for details", err)
		}
		recs, _ = recommend.Compose(ctx, facts, recommend.NewLocal())
	} else {
		recs, _ = recommend.Compose(ctx, recommend.Facts{Task: task}, recommend.NewLocal())
	}
	for _, r := range recs {
		out.Recommendations = append(out.Recommendations, report.FromRecommendation(r, d.Release))
	}
	var b strings.Builder
	if len(out.Recommendations) == 0 {
		out.Note = "No suggestions yet. Trove needs a few recorded sessions in this repo (install the hooks with `trove hooks install`), or a task type with --task."
		b.WriteString(out.Note + "\n")
	}
	for i, r := range out.Recommendations {
		fmt.Fprintf(&b, "%d. %s (confidence: %s; %s)\n   %s\n   %s\n", i+1, r.Title, r.Confidence, r.Label, r.Detail, recommendBasis(r))
	}
	if len(out.Recommendations) > 0 {
		out.Note = "Suggestions only; Trove has changed nothing."
		b.WriteString("\n" + out.Note + "\n")
	}
	return a.emit(out, b.String())
}

func recommendBasis(r report.Recommendation) string {
	switch r.Evidence {
	case "local":
		return "Based on your own data."
	case "benchmark":
		return "Based on a benchmark run (defaults " + r.DefaultsRelease + "); not yet checked on your own sessions."
	}
	return "A rule of thumb with no benchmark or measurement behind it yet (defaults " + r.DefaultsRelease + ")."
}
