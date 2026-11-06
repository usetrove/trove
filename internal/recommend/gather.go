package recommend

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/features"
	"github.com/usetrove/trove/internal/hooks"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// RecentSessions is how many of a repo's latest sessions the summary covers.
const RecentSessions = 30

// Gather collects the facts for one repo from the local database and config.
// repoKey may be empty (no repo known): the repo and session facts are then
// empty and only the user's experiments, optimizer states and task are filled.
// Everything it reads is already metadata; nothing is read from disk but the
// database.
func Gather(ctx context.Context, db *store.DB, cfg config.Config, repoKey, task string, now time.Time) (Facts, error) {
	f := Facts{Task: task, States: map[string]string{}}
	for _, n := range hooks.OptimizerNames {
		f.States[n] = cfg.OptimizerState(n)
	}
	if repoKey != "" {
		row, ok, err := db.Features().Repo(ctx, repoKey)
		if err != nil {
			return f, err
		}
		if ok {
			r := repoFromRow(row)
			f.Repo = &r
		}
		rows, err := db.Features().Sessions(ctx, repoKey, RecentSessions)
		if err != nil {
			return f, err
		}
		ss := make([]features.SessionFeatures, 0, len(rows))
		for _, s := range rows {
			ss = append(ss, features.SessionFeatures{FilesRead: s.FilesRead, FilesEdited: s.FilesEdited, RepeatReads: s.RepeatReads,
				ScoutCalls: s.ScoutCalls, Compactions: s.Compactions, Retries: s.Retries, ToolResults: s.ToolResults,
				LargeResults: s.LargeResults, AvgDupRatio: s.AvgDupRatio, ErrorShare: s.ErrorShare, StackShare: s.StackShare})
		}
		f.Sessions = Summarize(ss)
	}
	res, err := experiment.Evaluate(ctx, db, cfg.Experiment, experiment.DefaultPriors())
	if err != nil {
		return f, err
	}
	f.Experiments = res
	rolled, err := measure.Overrides(ctx, db)
	if err != nil {
		return f, err
	}
	for n := range rolled {
		f.RolledBack = append(f.RolledBack, n)
	}
	sort.Strings(f.RolledBack)
	if f.Tuning, err = ProposeTuning(ctx, db, cfg, repoKey, now); err != nil {
		return f, err
	}
	return f, nil
}

// repoFromRow turns a stored row back into bucketed features. A value outside
// the fixed sets (an edited database) becomes unknown rather than flowing on.
func repoFromRow(r store.RepoFeaturesRow) features.Repo {
	out := features.Repo{Size: features.SizeBucket(r.SizeBucket), TestFramework: features.TestUnknown, LangMix: []features.LangShare{}}
	for _, fw := range []features.TestFramework{features.TestGo, features.TestPytest, features.TestJest, features.TestVitest,
		features.TestMocha, features.TestCargo, features.TestJUnit, features.TestRSpec} {
		if string(fw) == r.TestFramework {
			out.TestFramework = fw
		}
	}
	for _, part := range strings.Split(r.LangMix, ",") {
		name, pct, ok := strings.Cut(part, ":")
		n, err := strconv.Atoi(pct)
		if ok && err == nil {
			out.LangMix = append(out.LangMix, features.LangShare{Lang: features.FileType(name), Pct: n})
		}
	}
	return out
}
