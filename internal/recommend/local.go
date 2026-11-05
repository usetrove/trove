package recommend

import (
	"context"
	"fmt"
	"strings"

	"github.com/usetrove/trove/internal/experiment"
)

// minSessionsForMedium is how many sessions of a repo a rule needs before its
// confidence may rise above low, whatever evidence it cites.
const minSessionsForMedium = 5

// Local is the open Provider: bundled defaults plus the user's own results.
type Local struct{ Defaults Defaults }

// NewLocal returns the local provider over the bundled defaults.
func NewLocal() Local { return Local{Defaults: BundledDefaults()} }

// Name implements Provider.
func (Local) Name() string { return "local" }

// Recommend implements Provider. It is a pure function of f and the defaults, so
// the same facts always give the same list. It never fails.
func (l Local) Recommend(_ context.Context, f Facts) ([]Recommendation, error) {
	var out []Recommendation
	handled := map[string]bool{}

	// 1. The user's own A/B results come first: they are measured.
	for _, r := range f.Experiments {
		if rec, ok := fromExperiment(r, f); ok {
			out = append(out, rec)
			handled[r.Optimizer] = true
		}
		// A verdict against an optimizer also silences the rules of thumb for it,
		// even when it is already off and there is nothing to say about it.
		switch r.Status {
		case experiment.CostsMore, experiment.WorseOutcomes, experiment.RolledBack:
			handled[r.Optimizer] = true
		}
	}
	// 2. Rules from the defaults file, for optimizers with no verdict of their own.
	for _, rule := range l.Defaults.Rules {
		if rule.Kind == KindOptimizer && (handled[rule.Name] || f.rolledBack(rule.Name)) {
			continue
		}
		if rule.Kind == KindOptimizer && f.state(rule.Name) != "off" {
			continue // already shadow or on: nothing to suggest from a rule of thumb
		}
		if !l.matches(rule, f) {
			continue
		}
		out = append(out, l.fromRule(rule, f))
	}
	Sort(out)
	return out, nil
}

// fromExperiment turns the user's own A/B result into a recommendation, if it
// says anything actionable. Not enough data, or no clear effect, says nothing:
// the rules below may still suggest staying in shadow.
func fromExperiment(r experiment.Result, f Facts) (Recommendation, bool) {
	base := Recommendation{ID: "experiment-" + r.Optimizer, Kind: KindOptimizer, Name: r.Optimizer, Evidence: EvidenceLocal, Label: "measured"}
	conf := Low
	label := "measured"
	if r.Cost != nil {
		conf = Confidence(r.Cost.Confidence)
		label = string(r.Cost.Label)
	}
	if conf != High && conf != Medium {
		conf = Low
	}
	base.Confidence, base.Label = conf, label
	st := f.state(r.Optimizer)
	switch r.Status {
	case experiment.Saves:
		if st == "on" || f.rolledBack(r.Optimizer) || r.Cost == nil {
			return Recommendation{}, false
		}
		base.Action = ActionEnable
		base.Reason = fmt.Sprintf("Your own A/B comparison (%d sessions with it on, %d off) found it cost %.1f%% less per session (range %.1f%% to %.1f%%), with outcomes no worse.",
			r.NOn, r.NOff, -r.Cost.Value*100, -r.Cost.High*100, -r.Cost.Low*100)
		return base, true
	case experiment.CostsMore, experiment.WorseOutcomes, experiment.RolledBack:
		if st == "off" {
			return Recommendation{}, false // already off; nothing to change
		}
		base.Action = ActionAvoid
		base.Reason = fmt.Sprintf("Your own A/B comparison (%d sessions with it on, %d off) says: %s. Leave it off for now.", r.NOn, r.NOff, statusPhrase(r.Status))
		return base, true
	}
	return Recommendation{}, false
}

func statusPhrase(s experiment.Status) string {
	switch s {
	case experiment.CostsMore:
		return "it cost more per session"
	case experiment.WorseOutcomes:
		return "outcomes got worse"
	}
	return "a guardrail rolled it back"
}

// matches tests a rule's conditions against the facts.
func (l Local) matches(r Rule, f Facts) bool {
	w := r.When
	if len(w.TaskIn) > 0 && !contains(w.TaskIn, f.Task) {
		return false
	}
	if w.usesRepo() {
		if f.Repo == nil {
			return false
		}
		if len(w.SizeIn) > 0 && !contains(w.SizeIn, string(f.Repo.Size)) {
			return false
		}
		if len(w.TestFrameworkIn) > 0 && !contains(w.TestFrameworkIn, string(f.Repo.TestFramework)) {
			return false
		}
		if len(w.LangIn) > 0 {
			found := false
			for _, ls := range f.Repo.LangMix {
				found = found || contains(w.LangIn, string(ls.Lang))
			}
			if !found {
				return false
			}
		}
	}
	if w.usesSessions() {
		a := f.Sessions
		need := max(w.MinSessions, l.Defaults.MinSessions)
		switch {
		case a.Sessions < need,
			a.FilesRead < w.MinFilesRead,
			a.RepeatReads < w.MinRepeatReads,
			a.Compactions < w.MinCompactions,
			a.Retries < w.MinRetries,
			a.ToolResults < w.MinToolResults,
			a.LargeResults < w.MinLargeResults,
			a.DupRatio < w.MinDupRatio,
			a.ErrorShare < w.MinErrorShare:
			return false
		}
	}
	return true
}

func (l Local) fromRule(r Rule, f Facts) Recommendation {
	conf := Low
	if r.Evidence == EvidenceBenchmark && (!r.When.usesSessions() || f.Sessions.Sessions >= minSessionsForMedium) {
		conf = Medium
	}
	return Recommendation{
		ID: r.ID, Kind: r.Kind, Name: r.Name, Action: r.Action,
		Reason: fillReason(r.Reason, f), Confidence: conf, Evidence: r.Evidence,
		Label: "estimated", Source: "local",
	}
}

// fillReason substitutes the template's placeholders with numbers and fixed names.
func fillReason(tpl string, f Facts) string {
	a := f.Sessions
	size, fw := "unknown-size", "unknown"
	if f.Repo != nil {
		size, fw = string(f.Repo.Size), string(f.Repo.TestFramework)
	}
	task := f.Task
	if task == "" {
		task = "unspecified"
	}
	return strings.NewReplacer(
		"{sessions}", fmt.Sprint(a.Sessions),
		"{files_read}", fmt.Sprintf("%.0f", a.FilesRead),
		"{repeat_reads}", fmt.Sprintf("%.1f", a.RepeatReads),
		"{large_results}", fmt.Sprintf("%.1f", a.LargeResults),
		"{dup_ratio}", fmt.Sprintf("%.0f%%", a.DupRatio*100),
		"{error_share}", fmt.Sprintf("%.0f%%", a.ErrorShare*100),
		"{compactions}", fmt.Sprintf("%.1f", a.Compactions),
		"{size}", size, "{framework}", fw, "{task}", task,
	).Replace(tpl)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
