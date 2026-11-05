// Package recommend is Trove's open, local recommender. It turns the features
// computed on the device (internal/features), the user's own A/B results
// (internal/experiment) and a bundled, versioned defaults file into a short list
// of suggestions: which optimizers to try in shadow mode or turn on, and which
// settings profile fits the task. Each suggestion carries a reason in plain
// English and a confidence level.
//
// It is deliberately simple and explainable: rules and lookup tables, no model.
// A suggestion only ever suggests; nothing here changes a setting (CLAUDE.md
// rule 6). Optimizers are suggested in shadow mode first, and "enable" is only
// said when the user's own measured A/B result supports it.
//
// The Provider interface is where a hosted settings bundle (a closed model, for
// users who opt in) can supply or override recommendations later. Nothing here
// makes a network call (rule 1). See docs/recommend.md.
package recommend

import (
	"context"
	"errors"
	"sort"
)

// Kind says what a recommendation is about.
type Kind string

// The kinds of recommendation.
const (
	KindOptimizer Kind = "optimizer" // an optimizer to try or turn on
	KindProfile   Kind = "profile"   // a settings profile for the task type
	KindTuning    Kind = "tuning"    // a per-repo parameter value (tune.go)
)

// Action is what Trove suggests doing.
type Action string

// The actions. Shadow comes first for anything that changes behavior.
const (
	ActionShadow     Action = "shadow"      // set the optimizer to shadow: estimate only, change nothing
	ActionEnable     Action = "enable"      // set it to on: the user's own measured A/B result supports it
	ActionAvoid      Action = "avoid"       // leave it off: it cost more or worsened outcomes here
	ActionUseProfile Action = "use_profile" // apply the settings profile with `trove settings`
	ActionTune       Action = "tune"        // use the tuned value for this repo
)

// Confidence is a plain label, not a probability.
type Confidence string

// The confidence levels.
const (
	High   Confidence = "high"
	Medium Confidence = "medium"
	Low    Confidence = "low"
)

func (c Confidence) rank() int {
	switch c {
	case High:
		return 3
	case Medium:
		return 2
	}
	return 1
}

// Evidence says what a recommendation rests on.
type Evidence string

// The kinds of evidence.
const (
	EvidenceLocal     Evidence = "local"     // the user's own sessions or A/B result
	EvidenceBenchmark Evidence = "benchmark" // a reproducible benchmark-lab result in the defaults file
	EvidenceNone      Evidence = "none"      // a rule of thumb with no result behind it yet
)

// Recommendation is one suggestion.
type Recommendation struct {
	ID         string     `json:"id"` // the rule that produced it
	Kind       Kind       `json:"kind"`
	Name       string     `json:"name"` // optimizer name, profile id or parameter name
	Action     Action     `json:"action"`
	Reason     string     `json:"reason"` // plain English, numbers only from features and results
	Confidence Confidence `json:"confidence"`
	Evidence   Evidence   `json:"evidence"`
	// Label is "measured" for a figure from the user's own A/B result and
	// "estimated" for anything derived from features or rules.
	Label string `json:"label"`
	// Source is who produced it: "local" or the name of another Provider.
	Source string `json:"source"`
	// Value is the suggested number for tuning recommendations.
	Value *float64 `json:"value,omitempty"`
}

// Provider produces recommendations from facts. LocalProvider is the open
// implementation; a hosted settings bundle would be another, used only if the
// user opts in.
type Provider interface {
	Name() string
	Recommend(ctx context.Context, f Facts) ([]Recommendation, error)
}

// Compose runs the providers in priority order and merges the results: when two
// providers recommend about the same kind and name, the earlier provider wins. A
// provider that fails is skipped (fail open, rule 4) and its error is returned
// next to whatever the others produced, so the caller can show both.
func Compose(ctx context.Context, f Facts, ps ...Provider) ([]Recommendation, error) {
	var out []Recommendation
	var errs []error
	seen := map[string]bool{}
	for _, p := range ps {
		recs, err := p.Recommend(ctx, f)
		if err != nil {
			errs = append(errs, errors.New(p.Name()+": "+err.Error()))
			continue
		}
		claimed := map[string]bool{}
		for _, r := range recs {
			k := string(r.Kind) + "/" + r.Name
			if seen[k] {
				continue
			}
			claimed[k] = true
			if r.Source == "" {
				r.Source = p.Name()
			}
			out = append(out, r)
		}
		for k := range claimed {
			seen[k] = true
		}
	}
	Sort(out)
	return out, errors.Join(errs...)
}

// Sort orders recommendations by confidence (highest first), then kind and name,
// so the same facts always give the same list.
func Sort(rs []Recommendation) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.Confidence.rank() != b.Confidence.rank() {
			return a.Confidence.rank() > b.Confidence.rank()
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
}
