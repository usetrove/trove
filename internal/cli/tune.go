package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/features"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/recommend"
	"github.com/usetrove/trove/internal/store"
)

// tuneRow is one tuned parameter as shown to the user.
type tuneRow struct {
	Optimizer string `json:"optimizer"`
	Param     string `json:"param"`
	Value     int    `json:"value"`
	Default   int    `json:"default"`
	State     string `json:"state"` // suggested, applied or reverted
	// BeforeRate is the share of trimmed outputs fetched back before the value was
	// proposed. It is measured from the hooks' own counts.
	BeforeRate float64 `json:"fetch_back_share_before"`
	NBefore    int     `json:"trimmed_outputs_before"`
	Label      string  `json:"label"`
}

type tuneOut struct {
	Repo      string                     `json:"repo"` // a hash, never the path
	Tuning    []tuneRow                  `json:"tuning"`
	Proposals []recommend.Recommendation `json:"proposals"`
	Note      string                     `json:"note"`
}

func newTune(a *App) *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "tune",
		Short: "See and apply per-repo tuning of the output trimmer",
		Long: "Trove can tune the output trimmer for one repo from that repo's own results: if your agent keeps fetching\n" +
			"back what the trimmer cut, it proposes keeping more of each output. Proposals are suggestions only. `trove tune\n" +
			"apply` puts them in effect for NEW sessions in that repo (a running session keeps what it started with), and Trove\n" +
			"goes back to the defaults by itself if the fetch-back share does not fall. Tuned values stay on this device.\n" +
			"The repo is identified by a salted hash of its folder; the folder name is never stored.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return tuneShow(c.Context(), a, repo) },
	}
	cmd.PersistentFlags().StringVar(&repo, "repo", "", "the repo's folder (default: the current folder)")
	cmd.AddCommand(&cobra.Command{Use: "apply", Short: "Put the suggested values in effect for new sessions in this repo", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return tuneApply(c.Context(), a, repo) }})
	cmd.AddCommand(&cobra.Command{Use: "reset", Short: "Forget the tuning for this repo and use the defaults", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return tuneReset(c.Context(), a, repo) }})
	return cmd
}

// tuneRepo opens the database and works out the repo key for the folder.
func tuneRepo(ctx context.Context, a *App, repo string) (dir string, cfg config.Config, db *store.DB, key string, err error) {
	dir, cfg, err = loadConfig(a)
	if err != nil {
		return
	}
	if repo == "" {
		if repo, err = os.Getwd(); err != nil {
			err = fmt.Errorf("cannot tell which folder you are in: %v. Pass the repo's folder with --repo", err)
			return
		}
	}
	if repo, err = filepath.Abs(repo); err != nil {
		return
	}
	if db, err = store.Open(dir); err != nil {
		err = fmt.Errorf("cannot open the database: %v. Run `trove doctor` for details", err)
		return
	}
	salt, serr := db.Features().Salt(ctx)
	if serr != nil {
		_ = db.Close()
		db, err = nil, fmt.Errorf("cannot read the repo salt: %v. Run `trove doctor` for details", serr)
		return
	}
	return dir, cfg, db, features.RepoKey(repo, salt), nil
}

func tuneRows(rows []store.TuningRow) []tuneRow {
	def := map[string]int{
		optimize.ParamMinBytes: optimize.DefaultTrimThresholds.MinBytes, optimize.ParamMinLines: optimize.DefaultTrimThresholds.MinLines,
		optimize.ParamMinTrimmedLines: optimize.DefaultTrimThresholds.MinTrimmedLines,
		optimize.ParamKeepHead:        optimize.DefaultTrimThresholds.KeepHead, optimize.ParamKeepTail: optimize.DefaultTrimThresholds.KeepTail,
	}
	out := []tuneRow{}
	for _, r := range rows {
		out = append(out, tuneRow{Optimizer: r.Optimizer, Param: r.Param, Value: r.Value, Default: def[r.Param], State: r.State,
			BeforeRate: r.BeforeRate, NBefore: r.NBefore, Label: "measured"})
	}
	return out
}

func tuneShow(ctx context.Context, a *App, repo string) error {
	_, cfg, db, key, err := tuneRepo(ctx, a, repo)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	now := time.Now()
	props, err := recommend.ProposeTuning(ctx, db, cfg, key, now)
	if err != nil {
		return fmt.Errorf("could not work out a proposal: %v. Run `trove doctor` for details", err)
	}
	rows := make([]store.TuningRow, 0, len(props))
	for _, p := range props {
		rows = append(rows, p.Row)
	}
	if err := db.Tuning().Suggest(ctx, rows); err != nil {
		return fmt.Errorf("could not save the suggestion: %v", err)
	}
	stored, err := db.Tuning().List(ctx, key)
	if err != nil {
		return err
	}
	out := tuneOut{Repo: key, Tuning: tuneRows(stored), Proposals: []recommend.Recommendation{}}
	for _, p := range props {
		out.Proposals = append(out.Proposals, p.Rec)
	}
	var b strings.Builder
	switch {
	case len(stored) == 0 && cfg.OptimizerState(config.OptimizerOutputTrimmer) != config.StateOn:
		out.Note = "Nothing to tune: the output trimmer is not on, so there is no data about it in this repo yet."
	case len(stored) == 0:
		out.Note = "Nothing to tune yet. Trove proposes a change only when the agent fetches back a large share of what the trimmer cut in this repo, and it needs enough trimmed outputs to be sure."
	}
	if out.Note != "" {
		b.WriteString(out.Note + "\n")
	}
	for _, r := range out.Tuning {
		fmt.Fprintf(&b, "%s.%s: %d (default %d) [%s]\n", r.Optimizer, r.Param, r.Value, r.Default, r.State)
	}
	for _, p := range out.Proposals {
		fmt.Fprintf(&b, "\n%s (%s confidence, %s): %s\n", p.Name, p.Confidence, p.Label, p.Reason)
	}
	switch {
	case len(props) > 0:
		b.WriteString("\nThese are suggestions; nothing has changed. Run `trove tune apply` to use them for new sessions in this repo.\n")
	}
	return a.emit(out, b.String())
}

func tuneApply(ctx context.Context, a *App, repo string) error {
	dir, cfg, db, key, err := tuneRepo(ctx, a, repo)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if cfg.OptimizerState(config.OptimizerOutputTrimmer) != config.StateOn {
		return fmt.Errorf("the output trimmer is not on, so tuning it would change nothing. Turn it on first (see `trove optimize`) or leave the defaults")
	}
	// Make sure the proposal is current before applying anything.
	props, err := recommend.ProposeTuning(ctx, db, cfg, key, time.Now())
	if err != nil {
		return err
	}
	rows := make([]store.TuningRow, 0, len(props))
	for _, p := range props {
		rows = append(rows, p.Row)
	}
	if err := db.Tuning().Suggest(ctx, rows); err != nil {
		return err
	}
	n, err := db.Tuning().Apply(ctx, key, time.Now())
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("there is nothing to apply for this repo. Run `trove tune` to see whether Trove has a proposal")
	}
	if err := syncHooks(a, dir, cfg); err != nil {
		return fmt.Errorf("applied, but could not refresh the hooks' snapshot: %v. Run `trove hooks sync`", err)
	}
	stored, _ := db.Tuning().List(ctx, key)
	out := tuneOut{Repo: key, Tuning: tuneRows(stored), Proposals: []recommend.Recommendation{},
		Note: fmt.Sprintf("Applied %d value(s). They take effect for new sessions in this repo; a session already running keeps what it started with. Trove goes back to the defaults by itself if the share of trimmed outputs fetched back does not fall.", n)}
	return a.emit(out, out.Note+"\n")
}

func tuneReset(ctx context.Context, a *App, repo string) error {
	dir, cfg, db, key, err := tuneRepo(ctx, a, repo)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err := db.Tuning().Reset(ctx, key); err != nil {
		return err
	}
	if err := syncHooks(a, dir, cfg); err != nil {
		return fmt.Errorf("reset, but could not refresh the hooks' snapshot: %v. Run `trove hooks sync`", err)
	}
	out := tuneOut{Repo: key, Tuning: []tuneRow{}, Proposals: []recommend.Recommendation{},
		Note: "Tuning for this repo is cleared. New sessions use the default settings."}
	return a.emit(out, out.Note+"\n")
}
