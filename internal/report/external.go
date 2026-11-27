package report

import (
	"context"
	"fmt"
	"strings"

	"github.com/usetrove/trove/internal/external"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// ExternalTool is one installed third-party tool: what the tool says it saves,
// next to what Trove measured. The two are never combined or reconciled here.
type ExternalTool struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	State        string `json:"state"` // "on" or "off"
	License      string `json:"license"`
	LicenseClass string `json:"license_class"`
	LicenseWarn  string `json:"license_warning,omitempty"`

	// Claimed is the tool's own claim, quoted from its documentation. It is
	// labeled "claimed" and Trove has not checked it.
	Claimed ExternalClaim `json:"claimed"`
	// Verdict is Trove's measured result in one line, shown next to Claimed. It is
	// built only from PromptTokens and Cost; the claim never enters it.
	Verdict ExternalVerdict `json:"verdict"`

	// Overhead is what the tool adds to every request while on (estimated).
	Overhead external.Charge `json:"overhead"`

	// PromptTokens compares provider-reported prompt tokens per request between
	// sessions with the tool on and off (measured). Nil when no off sessions exist.
	PromptTokens *measure.Corroboration `json:"prompt_tokens,omitempty"`
	// Cost compares the real cost per session on and off (measured). It counts
	// everything the tool causes, including extra turns. Nil when no off sessions exist.
	Cost *measure.NetCost `json:"cost,omitempty"`

	// Evidence says how the on and off sessions were chosen. Tools wired through
	// agent config cannot be switched per session, so the comparison is between
	// periods, which is weaker evidence than interleaved sessions.
	Evidence string `json:"evidence"`

	// Randomized is the randomized session test for a hook tool (`trove hook handoff`), once any session has been
	// assigned an arm. When it is set, the verdict comes from it, not from the before-and-after figures above.
	Randomized *TrialFigures `json:"randomized_trial,omitempty"`

	// RolledBack is Trove's reason for switching the tool off, if it did.
	RolledBack string `json:"rolled_back,omitempty"`
}

// ExternalClaim is a tool's own savings claim.
type ExternalClaim struct {
	Text      string `json:"text"`
	SourceURL string `json:"source_url"`
	Label     string `json:"label"` // always "claimed"
}

// ExternalVerdict is Trove's own verdict on a tool, in one line.
type ExternalVerdict struct {
	Text  string `json:"text"`
	Label string `json:"label,omitempty"` // "measured" when Text carries a figure, empty when it does not
}

// externalVerdict sums up the measured comparisons as relative changes, on versus off.
func externalVerdict(t ExternalTool) ExternalVerdict {
	var parts []string
	if c := t.Cost; c != nil && c.Enough && c.OffMeanUSD > 0 {
		s := "cost per session " + relChange(c.OnMeanUSD, c.OffMeanUSD)
		if !c.Cheaper && !c.Costlier {
			s += " (within chance)"
		}
		parts = append(parts, s)
	}
	if c := t.PromptTokens; c != nil && c.Verdict != measure.NotEnoughData && c.OffMean > 0 {
		s := "prompt tokens per request " + relChange(c.OnMean, c.OffMean)
		if !c.Reduced {
			s += " (not beyond chance)"
		}
		parts = append(parts, s)
	}
	if len(parts) > 0 {
		n := ""
		if c := t.Cost; c != nil {
			n = fmt.Sprintf(" (%d sessions on, %d off)", c.NOn, c.NOff)
		}
		return ExternalVerdict{Text: "with the tool on, " + strings.Join(parts, "; ") + n + "; measured", Label: "measured"}
	}
	if t.Cost == nil && t.PromptTokens == nil {
		return ExternalVerdict{Text: "nothing measured yet; it needs sessions with the tool off to compare against"}
	}
	return ExternalVerdict{Text: "nothing measured yet; " + measure.TextNotEnough}
}

// relChange is on against off as "20% lower", "5% higher" or "no change".
func relChange(on, off float64) string {
	d := (on - off) / off * 100
	switch {
	case d <= -0.5:
		return fmt.Sprintf("%.0f%% lower", -d)
	case d >= 0.5:
		return fmt.Sprintf("%.0f%% higher", d)
	}
	return "no change"
}

const externalEvidence = "Sessions with the tool on are compared with later sessions without it. Sessions differ in size and task, so this supports a conclusion but does not prove it."

// externalRandomEvidence is the evidence line for a hook tool in the randomized test.
const externalRandomEvidence = "Each new session was given the tool or not at random (trove hook handoff), so the two groups differ only by chance and by the tool. Method: docs/benchmark-methodology.md section 11."

// buildExternal assembles the rows for installed tools. It returns nothing when
// no tool is installed.
func buildExternal(ctx context.Context, db *store.DB, in Inputs) ([]ExternalTool, error) {
	recs, err := db.ExternalTools().List(ctx)
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	ms := in.ExternalManifests
	if ms == nil {
		if ms, err = external.Builtin(); err != nil {
			return nil, err
		}
	}
	byName := map[string]external.Manifest{}
	for _, m := range ms {
		byName[m.Name] = m
	}
	events, err := measure.GuardrailEvents(ctx, db, 200)
	if err != nil {
		return nil, err
	}
	var out []ExternalTool
	for _, rec := range recs {
		m, ok := byName[rec.Name]
		if !ok {
			continue // installed by a newer Trove; nothing honest to say about it
		}
		n := external.NoticeFor(m)
		t := ExternalTool{
			Name: m.Name, Version: rec.Version, State: rec.State, License: m.License,
			LicenseClass: string(n.Class), LicenseWarn: n.Warning,
			Claimed:  ExternalClaim{Text: m.Claimed.Text, SourceURL: m.Claimed.SourceURL, Label: "claimed"},
			Overhead: external.ChargeFor(m), Evidence: externalEvidence,
		}
		opt := m.OptimizerName()
		if t.PromptTokens, err = measure.Corroborate(ctx, db, opt, in.Config.Measure); err != nil {
			return nil, err
		}
		if t.Cost, err = measure.NetCostFor(ctx, db, opt, in.Config.Measure); err != nil {
			return nil, err
		}
		t.Verdict = externalVerdict(t)
		if m.ActsAs == external.ActsHook {
			f, err := trialFigures(ctx, db, in, opt)
			if err != nil {
				return nil, err
			}
			if f.HasSessions() {
				t.Randomized, t.Evidence = &f, externalRandomEvidence
				text, measured := trialVerdict(f)
				t.Verdict = ExternalVerdict{Text: text}
				if measured {
					t.Verdict.Label = "measured"
				}
			}
		}
		for _, g := range events { // newest first
			if g.Optimizer == opt && g.ClearedAt == nil {
				t.RolledBack = g.Reason
				break
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// externalHead is the tool's first lines in the text report: name, state and any license warning.
func externalHead(t ExternalTool) []string {
	lines := []string{fmt.Sprintf("%s v%s: %s (license %s, %s)", t.Name, t.Version, t.State, t.License, t.LicenseClass)}
	if t.LicenseWarn != "" {
		lines = append(lines, "  "+t.LicenseWarn)
	}
	return lines
}

// claimVerdictPairs are the two rows shown side by side: the tool's claim and Trove's verdict.
func claimVerdictPairs(t ExternalTool) [2][2]string {
	return [2][2]string{
		{"Claimed by the tool", fmt.Sprintf("%q (%s; not checked by Trove)", t.Claimed.Text, t.Claimed.SourceURL)},
		{"Measured by Trove", t.Verdict.Text},
	}
}

// externalLines renders the rest of one tool, after its claim and verdict, as indented lines.
func externalLines(t ExternalTool) []string {
	var lines []string
	// With a randomized test the before-and-after figures are secondary: shown only when there are some.
	if t.Randomized == nil || t.PromptTokens != nil {
		lines = append(lines, "  "+externalPromptText(t))
	}
	if t.Randomized == nil || t.Cost != nil {
		lines = append(lines, "  "+externalCostText(t))
	}
	lines = append(lines, fmt.Sprintf("  Overhead: %s tokens on every request while on (estimated; %s). It is part of every prompt, so the measured figures above already include it.",
		FormatTokens(float64(t.Overhead.PerRequest)), t.Overhead.Source))
	if t.Randomized != nil && t.Randomized.WeekNote != "" {
		lines = append(lines, "  Weekly limit: "+t.Randomized.WeekNote+".")
	}
	if t.PromptTokens != nil || t.Cost != nil || t.Randomized != nil {
		lines = append(lines, "  "+t.Evidence)
	}
	if t.RolledBack != "" {
		lines = append(lines, "  Switched off by Trove: "+t.RolledBack)
	}
	return lines
}

func externalPromptText(t ExternalTool) string {
	c := t.PromptTokens
	switch {
	case c == nil:
		return "Measured, prompt tokens per request: not available yet. It needs sessions with the tool off to compare against."
	case c.Verdict == measure.NotEnoughData:
		return fmt.Sprintf("Measured, prompt tokens per request: %s (%d sessions on, %d off).", measure.TextNotEnough, c.NOn, c.NOff)
	}
	return fmt.Sprintf("Measured, prompt tokens per request: %s on vs %s off (%d sessions on, %d off). %s.",
		FormatTokens(c.OnMean), FormatTokens(c.OffMean), c.NOn, c.NOff, capitalFirst(c.Text))
}

func externalCostText(t ExternalTool) string {
	c := t.Cost
	switch {
	case c == nil:
		return "Measured, cost per session: not available yet."
	case !c.Enough:
		return fmt.Sprintf("Measured, cost per session: %s (%d sessions on, %d off).", measure.TextNotEnough, c.NOn, c.NOff)
	}
	return fmt.Sprintf("Measured, cost per session: %s on vs %s off (%d sessions on, %d off). %s.",
		FormatUSD(c.OnMeanUSD), FormatUSD(c.OffMeanUSD), c.NOn, c.NOff, capitalFirst(c.Text))
}

func capitalFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
