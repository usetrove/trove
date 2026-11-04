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

	// RolledBack is Trove's reason for switching the tool off, if it did.
	RolledBack string `json:"rolled_back,omitempty"`
}

// ExternalClaim is a tool's own savings claim.
type ExternalClaim struct {
	Text      string `json:"text"`
	SourceURL string `json:"source_url"`
	Label     string `json:"label"` // always "claimed"
}

const externalEvidence = "Sessions with the tool on are compared with later sessions without it. Sessions differ in size and task, so this supports a conclusion but does not prove it."

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

// externalLines renders one tool as indented lines for the text report.
func externalLines(t ExternalTool) []string {
	lines := []string{fmt.Sprintf("%s v%s: %s (license %s, %s)", t.Name, t.Version, t.State, t.License, t.LicenseClass)}
	if t.LicenseWarn != "" {
		lines = append(lines, "  "+t.LicenseWarn)
	}
	lines = append(lines, fmt.Sprintf("  Claimed by the tool: %q (%s; not checked by Trove)", t.Claimed.Text, t.Claimed.SourceURL))
	lines = append(lines, "  "+externalPromptText(t))
	lines = append(lines, "  "+externalCostText(t))
	lines = append(lines, fmt.Sprintf("  Overhead: %s tokens on every request while on (estimated; %s). It is part of every prompt, so the measured figures above already include it.",
		FormatTokens(float64(t.Overhead.PerRequest)), t.Overhead.Source))
	if t.PromptTokens != nil || t.Cost != nil {
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
