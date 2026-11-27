package report

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/external"
	"github.com/usetrove/trove/internal/store"
)

// troveProjectLicense marks a manifest the Trove project wrote itself.
const troveProjectLicense = "Trove-project"

// ClaimsMethodURL is where the method behind every row is written down.
const ClaimsMethodURL = "https://github.com/usetrove/trove/blob/main/docs/benchmark-methodology.md#11-the-claims-table"

// ConsideredURL lists the tools Trove looked at and did not run, with the reason for each.
const ConsideredURL = "https://github.com/usetrove/trove/blob/main/docs/external.md#considered-and-not-run"

// Claims is the claims table: each tool's own claim next to what Trove measured in a randomized session test on
// this machine, for third-party tools and Trove's own optimizers alike. Rows with no clear effect are kept.
type Claims struct {
	GeneratedAt string     `json:"generated_at"` // date, UTC
	MethodURL   string     `json:"method_url"`
	Considered  string     `json:"considered_url"`
	Rows        []ClaimRow `json:"rows"`
	Notes       []string   `json:"notes"`
}

// ClaimRow is one tool or optimizer.
type ClaimRow struct {
	Name string `json:"name"`
	// Maker is "third-party" or "trove".
	Maker string `json:"maker"`
	// Claim is the tool's own claim (label "claimed", not checked by Trove) or, for Trove's own optimizers, Trove's
	// shadow estimate (label "estimated").
	Claim ExternalClaim `json:"claim"`
	// Status is the trial's verdict on its headline (tokens for a hook trial, cost for a gateway one).
	Status experiment.Status `json:"status"`
	Trial  TrialFigures      `json:"trial"`
	// The cells as rendered, so the JSON and the table say the same thing.
	WeeklyLimit string `json:"weekly_limit_text"`
	Usage       string `json:"usage_text"`
	PassRate    string `json:"pass_rate_text"`
	NOn         int    `json:"n_on"`
	NOff        int    `json:"n_off"`
	From        string `json:"from"`
	To          string `json:"to"`
}

// BuildClaims assembles the claims table from every optimizer that has been in a randomized session test. r is
// the report already built for the same inputs; it supplies the shadow estimates used as Trove's own claims.
func BuildClaims(ctx context.Context, db *store.DB, in Inputs, r Report) (Claims, error) {
	c := Claims{GeneratedAt: in.Now.UTC().Format(time.DateOnly), MethodURL: ClaimsMethodURL, Considered: ConsideredURL, Rows: []ClaimRow{}, Notes: claimsNotes()}
	if db == nil {
		return c, nil // no data directory yet: an empty table
	}
	names, err := experiment.Optimizers(ctx, db)
	if err != nil {
		return c, err
	}
	ms := in.ExternalManifests
	if ms == nil {
		if ms, err = external.Builtin(); err != nil {
			return c, err
		}
	}
	byOpt := map[string]external.Manifest{}
	for _, m := range ms {
		byOpt[m.OptimizerName()] = m
	}
	for _, n := range names {
		f, err := trialFigures(ctx, db, in, n)
		if err != nil {
			return c, err
		}
		row := ClaimRow{Name: n, Maker: "trove", Trial: f}
		if m, ok := byOpt[n]; ok {
			row.Name, row.Maker = m.Name, "third-party"
			if m.License == troveProjectLicense {
				row.Maker = "trove" // terse-rules: the example rules file, written by the Trove project
			}
			row.Claim = ExternalClaim{Text: m.Claimed.Text, SourceURL: m.Claimed.SourceURL, Label: "claimed"}
		} else if strings.HasPrefix(n, external.Prefix) {
			continue // a tool this Trove has no manifest for: nothing honest to say about its claim
		} else {
			row.Claim = troveClaim(r, n)
		}
		fillCells(&row)
		c.Rows = append(c.Rows, row)
	}

	return c, nil
}

// troveClaim is Trove's own estimate for one of its optimizers: the shadow estimate from the report, if any.
func troveClaim(r Report, name string) ExternalClaim {
	for _, o := range r.Optimizers.Rows {
		if o.Name != name {
			continue
		}
		fig, what := o.WouldSave, "would have saved"
		if fig == nil {
			fig, what = o.Saved, "saved"
		}
		if fig != nil {
			return ExternalClaim{Text: fmt.Sprintf("Trove's shadow estimate: %s %s in the report period", what, FormatUSD(fig.Value)),
				SourceURL: "docs/techniques.md", Label: string(fig.Label)}
		}
	}
	return ExternalClaim{Text: "none: Trove published no estimate for it before the test", SourceURL: "docs/techniques.md"} // no number, so no label
}

// fillCells renders a row's cells from its trial.
func fillCells(row *ClaimRow) {
	f := row.Trial
	if f.FirstAt > 0 {
		row.From = time.UnixMilli(f.FirstAt).UTC().Format(time.DateOnly)
		row.To = time.UnixMilli(f.LastAt).UTC().Format(time.DateOnly)
	}
	m := f.main()
	if m == nil {
		row.Status = experiment.NotEnoughData
		return
	}
	row.Status, row.NOn, row.NOff = m.Status, m.NOn, m.NOff
	switch {
	case !f.Hook:
		row.WeeklyLimit = "not applicable: the gateway splits these sessions, which use an API key and so no plan limit"
	case f.Week == nil:
		row.WeeklyLimit = "not available: " + f.WeekNote
	case f.Week.Cost == nil:
		row.WeeklyLimit = "not enough data: " + f.Week.Reason
	default:
		s := changeText(*f.Week.Cost)
		if f.Week.HeadlinePoints != nil {
			s += "; " + limitPointsText(*f.Week.HeadlinePoints, "weekly limit")
		}
		row.WeeklyLimit = s + "; " + effectWord(*f.Week.Cost) + ", " + f.WeekBasis
	}
	switch {
	case m.Cost == nil:
		row.Usage = "not enough data: " + m.Reason
	case f.Tokens != nil:
		row.Usage = "tokens per session " + changeText(*m.Cost) + "; " + effectWord(*m.Cost) + ", measured"
	default:
		row.Usage = "cost per session at API list prices " + changeText(*m.Cost) + "; " + effectWord(*m.Cost) + ", estimated from measured usage"
	}
	if p := passText(m); p != "" {
		row.PassRate = p
	} else if m.TestFail != nil {
		row.PassRate = fmt.Sprintf("not enough data: %d and %d sessions ran tests", m.TestFail.OnSessions, m.TestFail.OffSessions)
	} else {
		row.PassRate = "not compared: the gateway does not see test runs"
	}
}

// effectWord says whether a range rules out no change.
func effectWord(m experiment.MetricResult) string {
	switch {
	case m.High < 0:
		return "lower"
	case m.Low > 0:
		return "higher"
	}
	return "no clear effect"
}

func claimsNotes() []string {
	return []string{
		"Every row is one randomized session test on this machine: each new session was given the tool or optimizer, or not, by a hash of its key. Sessions are counted under the arm they were given, whether or not it acted.",
		"Weekly-limit figures are estimates for one account, from this machine's meter readings (docs/limit-fit.md). They are not official weights.",
		"Ranges are 95% bootstrap ranges. \"No clear effect\" means the range includes no change; that is a result, not a gap.",
		"A tool's claim is quoted from its own documentation and is not checked by Trove. Trove's own claim is its shadow estimate, made before the test.",
		"Tools Trove looked at and did not run, and why: " + ConsideredURL + ".",
	}
}

// RenderClaimsMarkdown is the claims table as Markdown, for publishing.
func RenderClaimsMarkdown(c Claims) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Token-saving tools: their claims next to Trove's measurements\n\nGenerated %s by `trove report claims --markdown`. Method: [benchmark methodology, section 11](%s).\n\n", c.GeneratedAt, c.MethodURL)
	if len(c.Rows) == 0 {
		b.WriteString("No randomized session test has run yet.\n")
	} else {
		b.WriteString("| Tool | Made by | Its claim | Effect on the weekly limit per session | Tokens or cost per session | Pass-rate change | Sessions (on / off) | Dates | Method |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
		for _, r := range c.Rows {
			claim := mdCell(r.Claim.Text)
			if strings.HasPrefix(r.Claim.SourceURL, "http") {
				claim = fmt.Sprintf("[%s](%s)", claim, r.Claim.SourceURL)
			}
			if r.Claim.Label != "" {
				claim += " (" + r.Claim.Label + ")"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %d / %d | %s | [method](%s) |\n",
				mdCell(r.Name), r.Maker, claim, mdCell(orDash(r.WeeklyLimit)), mdCell(orDash(r.Usage)), mdCell(orDash(r.PassRate)),
				r.NOn, r.NOff, dateRange(r), c.MethodURL)
		}
	}
	b.WriteString("\n")
	for _, n := range c.Notes {
		b.WriteString("- " + n + "\n")
	}
	return b.String()
}

// RenderClaimsText is the claims table for a terminal: one block per row.
func RenderClaimsText(c Claims, width int) string {
	var b strings.Builder
	b.WriteString(heading("Claims next to measurements (randomized session tests on this machine)"))
	if len(c.Rows) == 0 {
		b.WriteString("  No randomized session test has run yet.\n")
	}
	for _, r := range c.Rows {
		b.WriteString(wrap(fmt.Sprintf("%s (%s): %s, %d sessions on / %d off, %s", r.Name, r.Maker, claimStatus(r.Status), r.NOn, r.NOff, dateRange(r)), width, "  ", "    ") + "\n")
		const col = "    %-13s "
		pad := strings.Repeat(" ", len(fmt.Sprintf(col, "")))
		for _, p := range [][2]string{
			{"Claim", claimText(r.Claim)},
			{"Weekly limit", orDash(r.WeeklyLimit)},
			{"Usage", orDash(r.Usage)},
			{"Tests", orDash(r.PassRate)},
		} {
			b.WriteString(wrap(p[1], width, fmt.Sprintf(col, p[0]), pad) + "\n")
		}
	}
	b.WriteString("\n")
	for _, n := range c.Notes {
		b.WriteString(wrap(n, width, "  ", "  ") + "\n")
	}
	b.WriteString(wrap("Method: "+c.MethodURL, width, "  ", "  ") + "\n")
	return b.String()
}

// claimStatus is a trial's verdict in words that fit tokens as well as dollars.
func claimStatus(s experiment.Status) string {
	switch s {
	case experiment.Saves:
		return "lower with it on"
	case experiment.CostsMore:
		return "higher with it on"
	case experiment.WorseOutcomes:
		return "worse outcomes with it on"
	case experiment.NoClearEffect:
		return "no clear effect"
	}
	return "not enough data yet"
}

func claimText(c ExternalClaim) string {
	if c.Label == "" {
		return fmt.Sprintf("%s (%s)", c.Text, c.SourceURL)
	}
	return fmt.Sprintf("%q (%s; %s)", c.Text, c.Label, c.SourceURL)
}

func dateRange(r ClaimRow) string {
	switch r.From {
	case "":
		return "-"
	case r.To:
		return r.From
	}
	return r.From + " to " + r.To
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// mdCell keeps a value inside one Markdown table cell.
func mdCell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}
