// Package suggest turns recorded request metadata into recommendations that
// Trove never applies on its own: a cheaper model for small tasks, the Batch
// API for work that can wait, and a lower reasoning effort.
//
// Everything here is advice. It reads only the metadata Trove already stored
// (model names, token counts, cost, flags), never prompt text, and every number
// is labeled "estimated". Where Trove cannot honestly put a dollar figure on a
// suggestion it says so instead of guessing (CLAUDE.md rule 9).
//
// Provider facts used, verified 2026-10-29 against the Anthropic docs:
//   - Message Batches API: 50% discount on all usage, most batches finish within
//     1 hour, results available when done or after 24 hours, and batches expire
//     if unfinished after 24 hours
//     (https://platform.claude.com/docs/en/build-with-claude/batch-processing).
//   - Reasoning effort: output_config.effort, levels low, medium, high, xhigh,
//     max; the default is high except Claude Opus 5.5 and Claude Haiku 5.5, which
//     default to medium; lower effort spends fewer tokens in exchange for some
//     capability (https://platform.claude.com/docs/en/build-with-claude/effort).
package suggest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/pricing"
)

// Suggestion kinds.
const (
	KindModelChoice     = "model_choice"
	KindBatchAPI        = "batch_api"
	KindReasoningEffort = "reasoning_effort"
)

// Suggestion is one recommendation for the report.
type Suggestion struct {
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	// EstSavingsUSD is nil when no honest figure exists; Scenario then says what
	// a change would be worth per unit instead.
	EstSavingsUSD *float64 `json:"est_savings_usd"`
	Scenario      string   `json:"scenario,omitempty"`
	Confidence    string   `json:"confidence"` // high, medium or low
	Label         string   `json:"label"`      // always "estimated"
	Requests      int      `json:"requests"`
}

// Querier is the part of *sql.DB the suggestions need.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Thresholds. A suggestion built on a handful of requests would be noise.
const (
	minRequests       = 20
	minOutputShare    = 0.40 // output tokens' share of spend before effort is worth mentioning
	interactiveAgents = "'claude_code','codex'"
)

// Generate builds the suggestions for requests with from <= ts < to. tbl may be
// nil, in which case only suggestions that need no price list are produced.
func Generate(ctx context.Context, db Querier, tbl *pricing.Table, from, to time.Time) ([]Suggestion, error) {
	var out []Suggestion
	m, err := modelChoice(ctx, db, from, to)
	if err != nil {
		return nil, err
	}
	out = append(out, m...)
	if tbl != nil {
		b, err := batchAPI(ctx, db, tbl, from, to)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
		e, err := reasoningEffort(ctx, db, tbl, from, to)
		if err != nil {
			return nil, err
		}
		out = append(out, e...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].EstSavingsUSD, out[j].EstSavingsUSD
		switch {
		case a != nil && b != nil && *a != *b:
			return *a > *b
		case (a == nil) != (b == nil):
			return a != nil
		}
		return out[i].Title < out[j].Title
	})
	return out, nil
}

// modelChoice aggregates the analyzer's expensive_model_small_task findings:
// turns with a small input and a small output that ran on a frontier model.
func modelChoice(ctx context.Context, db Querier, from, to time.Time) ([]Suggestion, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT model, findings FROM requests WHERE ts >= ? AND ts < ? AND findings LIKE '%expensive_model_small_task%'`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type key struct{ model, alt string }
	type agg struct {
		n      int
		usd    float64
		priced int
	}
	groups := map[key]*agg{}
	for rows.Next() {
		var model, raw string
		if err := rows.Scan(&model, &raw); err != nil {
			return nil, err
		}
		var fs []struct {
			Detector string         `json:"detector"`
			USD      *float64       `json:"usd"`
			Detail   map[string]any `json:"detail"`
		}
		if json.Unmarshal([]byte(raw), &fs) != nil {
			continue
		}
		for _, f := range fs {
			if f.Detector != "expensive_model_small_task" {
				continue
			}
			alt, _ := f.Detail["alternative"].(string)
			g := groups[key{model, alt}]
			if g == nil {
				g = &agg{}
				groups[key{model, alt}] = g
			}
			g.n++
			if f.USD != nil {
				g.usd += *f.USD
				g.priced++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Suggestion
	for k, g := range groups {
		if g.n < minRequests || k.alt == "" || g.priced == 0 {
			continue
		}
		usd := g.usd
		out = append(out, Suggestion{
			Kind:  KindModelChoice,
			Title: fmt.Sprintf("Use %s for small tasks instead of %s", k.alt, k.model),
			Detail: fmt.Sprintf("%d turns had a small prompt and a small reply but ran on %s. Trove cannot check whether %s would have handled them as well, so try it on a few of these tasks first.",
				g.n, k.model, k.alt),
			EstSavingsUSD: &usd, Confidence: "low", Label: "estimated", Requests: g.n,
		})
	}
	return out, nil
}

// batchAPI looks for non-streaming API traffic from non-interactive callers: the
// kind of work that could often go through the Batch API at a discount if it
// can wait.
func batchAPI(ctx context.Context, db Querier, tbl *pricing.Table, from, to time.Time) ([]Suggestion, error) {
	rows, err := db.QueryContext(ctx, `SELECT model, COUNT(*), SUM(cost_usd) FROM requests
		WHERE ts >= ? AND ts < ? AND provider = 'anthropic' AND endpoint = '/v1/messages' AND streamed = 0
		AND status_code BETWEEN 200 AND 299 AND cost_usd IS NOT NULL AND service_tier != 'batch'
		AND agent NOT IN (`+interactiveAgents+`) GROUP BY model`, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var n int
	var saving, spend float64
	for rows.Next() {
		var model string
		var count int
		var cost float64
		if err := rows.Scan(&model, &count, &cost); err != nil {
			return nil, err
		}
		e, _ := tbl.Lookup(pricing.Anthropic, model, to)
		d, ok := batchDiscount(e)
		if !ok {
			continue // no batch price listed for this model: no figure
		}
		n += count
		spend += cost
		saving += cost * d
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if n < minRequests {
		return nil, nil
	}
	conf := "low"
	if n >= 200 {
		conf = "medium"
	}
	return []Suggestion{{
		Kind:  KindBatchAPI,
		Title: "Send work that can wait through the Batch API",
		Detail: fmt.Sprintf("%d non-streaming requests from scripts or custom agents cost less on the Batch API (%.0f%% off on the listed models), which finishes most batches within an hour and always within 24. "+
			"Only worth it for work nobody is waiting on. Cache hits in batches are best-effort, so savings on cache-heavy prompts may be smaller.",
			n, 100*saving/spend),
		EstSavingsUSD: &saving, Confidence: conf, Label: "estimated", Requests: n,
	}}, nil
}

// batchDiscount returns the fraction saved by batch pricing for e, or false if
// the table has no uniform batch multiplier for the model.
func batchDiscount(e *pricing.Entry) (float64, bool) {
	if e == nil || e.Batch == nil || e.Batch.Input == nil || e.Batch.Output == nil || e.Batch.Input.Cmp(*e.Batch.Output) != 0 {
		return 0, false
	}
	m, err := strconv.ParseFloat(e.Batch.Input.String(), 64)
	if err != nil || m <= 0 || m >= 1 {
		return 0, false
	}
	return 1 - m, true
}

// effortDefaults lists the models that support output_config.effort and the
// level each uses when none is set. Verified 2026-10-29
// (https://platform.claude.com/docs/en/build-with-claude/effort).
var effortDefaults = map[string]string{
	"claude-fable-5-1": "high", "claude-mythos-5-1": "high", "claude-fable-5": "high", "claude-mythos-5": "high",
	"claude-mythos-preview": "high", "claude-opus-5-5": "medium", "claude-opus-5": "high",
	"claude-sonnet-5-5": "high", "claude-sonnet-5": "high", "claude-haiku-5-5": "medium",
	"claude-opus-4-8": "high", "claude-opus-4-7": "high", "claude-opus-4-6": "high",
	"claude-sonnet-4-6": "high", "claude-opus-4-5": "high",
}

var snapshot = regexp.MustCompile(`^[-@](\d{8}|latest)$`)

func effortDefault(model string) (string, bool) {
	best := ""
	for id := range effortDefaults {
		if strings.HasPrefix(model, id) && len(id) > len(best) && (model == id || snapshot.MatchString(model[len(id):])) {
			best = id
		}
	}
	return effortDefaults[best], best != ""
}

var stepDown = map[string]string{"max": "xhigh", "xhigh": "high", "high": "medium", "medium": "low"}

// reasoningEffort flags models where output tokens are a large share of spend.
// Docs say a lower effort level spends fewer tokens but publish no figure, so
// the suggestion carries a per-10% scenario rather than a savings estimate.
func reasoningEffort(ctx context.Context, db Querier, tbl *pricing.Table, from, to time.Time) ([]Suggestion, error) {
	rows, err := db.QueryContext(ctx, `SELECT model, COUNT(*), SUM(output_tokens), SUM(cost_usd) FROM requests
		WHERE ts >= ? AND ts < ? AND provider = 'anthropic' AND status_code BETWEEN 200 AND 299 AND cost_usd IS NOT NULL
		GROUP BY model`, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Suggestion
	for rows.Next() {
		var model string
		var count int
		var outTok int64
		var cost float64
		if err := rows.Scan(&model, &count, &outTok, &cost); err != nil {
			return nil, err
		}
		def, ok := effortDefault(model)
		next := stepDown[def]
		e, _ := tbl.Lookup(pricing.Anthropic, model, to)
		if !ok || next == "" || count < minRequests || cost <= 0 || e == nil {
			continue
		}
		price, err := strconv.ParseFloat(e.Standard.Output.String(), 64)
		if err != nil {
			continue
		}
		outCost := float64(outTok) * price / 1e6
		share := outCost / cost
		if share < minOutputShare {
			continue
		}
		per10 := outCost * 0.10
		out = append(out, Suggestion{
			Kind:  KindReasoningEffort,
			Title: fmt.Sprintf("Try %q effort on %s", next, model),
			Detail: fmt.Sprintf("Output tokens are about %.0f%% of your %s spend over %d requests. Effort (output_config.effort) defaults to %q on this model; a lower level uses fewer tokens in exchange for some capability. "+
				"Anthropic publishes no savings figure, so run a few of your own tasks at %q and compare quality before relying on it.",
				100*share, model, count, def, next),
			Scenario:   fmt.Sprintf("every 10%% fewer output tokens would save about $%.2f", per10),
			Confidence: "low", Label: "estimated", Requests: count,
		})
	}
	return out, rows.Err()
}
