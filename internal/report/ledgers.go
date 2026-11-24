package report

import (
	"context"
	"fmt"
	"strings"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// Labels in this file (PRINCIPLES.md rules 6 and 9): token counts copied from the log (cache writes,
// output, thinking where the log has it) are measured. Which cause a rebuild had is inferred, so the
// per-cause figures are estimated. Text and tool-input tokens are estimated from byte sizes, and the
// thinking carried in context is an estimated upper bound.

// Rebuilds is the cache-rebuild ledger for Claude Code history in the period (see docs/analyze.md).
type Rebuilds struct {
	Count  measure.Figure `json:"count"`              // rebuilds: measured (threshold on measured numbers)
	Tokens measure.Figure `json:"cache_write_tokens"` // measured
	// ShareOfWrites is rebuild tokens as a percent of all cache writes in the period (measured).
	ShareOfWrites measure.Figure `json:"share_of_cache_writes_pct"`
	ByCause       []RebuildCause `json:"by_cause"`
	// SessionStarts are main conversations' first requests: they build a cache rather than rebuild one,
	// so they are listed but not counted above.
	SessionStarts      measure.Figure `json:"session_starts"`
	SessionStartTokens measure.Figure `json:"session_start_tokens"`
	FactorThreshold    int            `json:"threshold_factor"`
	MinTokensThreshold int            `json:"threshold_min_tokens"`
	DefaultTTLRebuilds int            `json:"idle_with_default_ttl"` // idle verdicts that used a documented default TTL
}

// RebuildCause is one cause's share of the rebuilds.
type RebuildCause struct {
	Cause  string         `json:"cause"`
	Count  measure.Figure `json:"count"`              // estimated: the cause is inferred
	Tokens measure.Figure `json:"cache_write_tokens"` // estimated attribution of measured tokens
	Share  measure.Figure `json:"share_pct"`          // of rebuild tokens, estimated
}

// OutputMix is the output ledger for Claude Code history in the period.
type OutputMix struct {
	Total     OutputRow   `json:"total"`
	ByPhase   []OutputRow `json:"by_phase"`
	ByModel   []OutputRow `json:"by_model"`
	ByEffort  []OutputRow `json:"by_effort"`
	ByVersion []OutputRow `json:"by_version"`
	// Carried is earlier thinking still in context, summed over the requests on models with preserved
	// thinking: an estimated upper bound of the input tokens it took.
	Carried      measure.Figure          `json:"carried_thinking_tokens"`
	CarriedShare measure.Figure          `json:"carried_share_of_input_pct"`
	Ratios       []analyze.BytesPerToken `json:"bytes_per_token"`
}

// OutputRow splits output for one key. Output and Unsplit are measured; Thinking is measured unless
// the log lacked a count for some requests; Text and ToolInput are estimated.
type OutputRow struct {
	Key       string         `json:"key"`
	Requests  int            `json:"requests"`
	Output    measure.Figure `json:"output_tokens"`
	Thinking  measure.Figure `json:"thinking_tokens"`
	Text      measure.Figure `json:"text_tokens"`
	ToolInput measure.Figure `json:"tool_input_tokens"`
	Unsplit   measure.Figure `json:"unsplit_tokens"`
}

// maxOutputRows caps the by-model and by-version tables; the rest is folded into one row.
const maxOutputRows = 6

func fig(v float64, l measure.Label, n int) measure.Figure {
	return measure.Figure{Value: v, Label: l, N: n}
}

func pct(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return round6(float64(part) / float64(whole) * 100)
}

// buildLedgers runs both ledgers over the period. Either result is nil when there is nothing to show.
func buildLedgers(ctx context.Context, db *store.DB, w measure.Window) (*Rebuilds, *OutputMix, error) {
	reqs, err := measure.HistoryLedger(ctx, db, w)
	if err != nil || len(reqs) == 0 {
		return nil, nil, err
	}
	from, to := w.From.UnixMilli(), w.To.UnixMilli()
	var writes int64
	for i := range reqs {
		if r := &reqs[i]; r.TS >= from && r.TS < to {
			writes += r.CacheWrite5m + r.CacheWrite1h + r.CacheWriteNA
		}
	}
	rb := rebuildsFrom(analyze.SummarizeRebuilds(analyze.RebuildLedger(reqs), from, to, writes))
	entries, ratios := analyze.OutputLedger(reqs)
	om := outputFrom(analyze.SummarizeOutput(entries, ratios, from, to))
	return rb, om, nil
}

func rebuildsFrom(s analyze.RebuildSummary) *Rebuilds {
	if len(s.Rows) == 0 {
		return nil
	}
	rb := &Rebuilds{
		Count:              fig(float64(s.RebuildCount), measure.Measured, s.RebuildCount),
		Tokens:             fig(float64(s.RebuildTokens), measure.Measured, s.RebuildCount),
		ShareOfWrites:      fig(pct(s.RebuildTokens, s.CacheWriteTokens), measure.Measured, s.RebuildCount),
		SessionStarts:      fig(0, measure.Measured, 0),
		SessionStartTokens: fig(0, measure.Measured, 0),
		FactorThreshold:    analyze.RebuildFactor,
		MinTokensThreshold: analyze.RebuildMinTokens,
		ByCause:            []RebuildCause{},
	}
	for _, row := range s.Rows {
		if row.Cause == analyze.CauseSessionStart {
			rb.SessionStarts = fig(float64(row.Count), measure.Measured, row.Count)
			rb.SessionStartTokens = fig(float64(row.Tokens), measure.Measured, row.Count)
			continue
		}
		rb.DefaultTTLRebuilds += row.TTLGuess
		rb.ByCause = append(rb.ByCause, RebuildCause{
			Cause:  row.Cause,
			Count:  fig(float64(row.Count), measure.Estimated, row.Count),
			Tokens: fig(float64(row.Tokens), measure.Estimated, row.Count),
			Share:  fig(round6(row.Share*100), measure.Estimated, row.Count),
		})
	}
	return rb
}

func outputRow(b analyze.OutputBucket) OutputRow {
	think := measure.Measured
	if b.ThinkingEstimated > 0 {
		think = measure.Estimated
	}
	return OutputRow{
		Key:       b.Key,
		Requests:  b.Requests,
		Output:    fig(float64(b.Output), measure.Measured, b.Requests),
		Thinking:  fig(float64(b.Thinking), think, b.Requests),
		Text:      fig(float64(b.Text), measure.Estimated, b.Requests),
		ToolInput: fig(float64(b.ToolInput), measure.Estimated, b.Requests),
		Unsplit:   fig(float64(b.Unsplit), measure.Measured, b.Requests),
	}
}

// outputRows converts buckets, folding everything past limit (0 = no limit) into one row named rest.
func outputRows(bs []analyze.OutputBucket, limit int, rest string) []OutputRow {
	if limit > 0 && len(bs) > limit {
		other := analyze.OutputBucket{Key: rest}
		for _, b := range bs[limit-1:] {
			other.Requests += b.Requests
			other.Output += b.Output
			other.Thinking += b.Thinking
			other.ThinkingEstimated += b.ThinkingEstimated
			other.Text += b.Text
			other.ToolInput += b.ToolInput
			other.Unsplit += b.Unsplit
		}
		bs = append(append([]analyze.OutputBucket(nil), bs[:limit-1]...), other)
	}
	out := make([]OutputRow, 0, len(bs))
	for _, b := range bs {
		out = append(out, outputRow(b))
	}
	return out
}

func outputFrom(s analyze.OutputSummary) *OutputMix {
	if s.Total.Output-s.Total.Unsplit <= 0 {
		return nil // nothing could be split: rows read before the parser recorded sizes
	}
	return &OutputMix{
		Total:        outputRow(s.Total),
		ByPhase:      outputRows(s.ByPhase, 0, ""),
		ByModel:      outputRows(s.ByModel, maxOutputRows, "other models"),
		ByEffort:     outputRows(s.ByEffort, 0, ""),
		ByVersion:    outputRows(s.ByVersion, maxOutputRows, "other versions"),
		Carried:      fig(float64(s.CarriedTokens), measure.Estimated, s.CarriedRequests),
		CarriedShare: fig(pct(s.CarriedTokens, s.CarriedContext), measure.Estimated, s.CarriedRequests),
		Ratios:       s.Ratios,
	}
}

var causeTitles = map[string]string{
	analyze.CauseIdle:        "Idle past the cache lifetime",
	analyze.CauseModel:       "Model changed",
	analyze.CauseSubagent:    "Subagent's first request",
	analyze.CauseCompaction:  "Compaction",
	analyze.CauseVersion:     "Claude Code version changed",
	analyze.CauseEffort:      "Effort level changed",
	analyze.CauseFastMode:    "Fast mode switched",
	analyze.CauseUnexplained: "Unexplained",
}

var phaseTitles = map[string]string{
	"prompt":        "After a prompt",
	"tool_result":   "Tool-result continuation",
	"compact":       "After compaction",
	"other":         "Other",
	analyze.Unknown: "(not recorded)",
}

func renderRebuilds(b *strings.Builder, rb *Rebuilds, o Options) {
	b.WriteString("\n" + heading("Cache rebuilds (Claude Code history)"))
	if rb.Count.Value == 0 {
		b.WriteString(wrap("No rebuilds found (measured).", o.width(), "  ", "  ") + "\n")
	} else {
		n := int(rb.Count.Value)
		b.WriteString(wrap(fmt.Sprintf("%d %s wrote %s tokens to the cache again (measured), %s of all cache writes in this period.",
			n, plural(n, "rebuild", "rebuilds"), FormatTokens(rb.Tokens.Value), FormatPercent(rb.ShareOfWrites.Value)), o.width(), "  ", "  ") + "\n")
		data := [][]string{{"Cause (estimated)", "Count", "Tokens", "Share"}}
		for _, c := range rb.ByCause {
			data = append(data, []string{causeTitles[c.Cause], fmt.Sprintf("%d", int(c.Count.Value)), FormatTokens(c.Tokens.Value), FormatPercent(c.Share.Value)})
		}
		b.WriteString(table(data, o.width()))
	}
	notes := []string{fmt.Sprintf("A rebuild is a cache write over %s tokens and over %d times the content that was new since the agent's previous request.",
		FormatTokens(float64(rb.MinTokensThreshold)), rb.FactorThreshold)}
	if rb.SessionStarts.Value > 0 {
		n := int(rb.SessionStarts.Value)
		notes = append(notes, fmt.Sprintf("Not counted: %d %s, which build a cache rather than rebuild one (%s tokens, measured).",
			n, plural(n, "session start", "session starts"), FormatTokens(rb.SessionStartTokens.Value)))
	}
	if rb.DefaultTTLRebuilds > 0 {
		notes = append(notes, fmt.Sprintf("%d idle verdicts used Claude Code's documented default cache lifetime because the log had not shown one yet.", rb.DefaultTTLRebuilds))
	}
	for _, c := range rb.ByCause {
		if c.Cause == analyze.CauseUnexplained {
			notes = append(notes, "Unexplained covers causes the log does not show (an MCP server, plugin or tool list that changed) as well as possible Claude Code bugs.")
		}
	}
	for _, n := range notes {
		b.WriteString(wrap(n, o.width(), "  ", "  ") + "\n")
	}
}

func outputTable(b *strings.Builder, head string, rows []OutputRow, titles map[string]string, o Options) {
	data := [][]string{{head, "Output", "Thinking", "Text", "Tool input"}}
	for _, r := range rows {
		k := r.Key
		if t, ok := titles[k]; ok {
			k = t
		}
		if r.Unsplit.Value > 0 {
			k += " *"
		}
		if r.Unsplit.Value >= r.Output.Value { // nothing here could be split: no number beats a false zero
			data = append(data, []string{orUnknown(k), FormatTokens(r.Output.Value), "-", "-", "-"})
			continue
		}
		think := FormatTokens(r.Thinking.Value)
		if r.Thinking.Label == measure.Estimated {
			think = "~" + think
		}
		data = append(data, []string{orUnknown(k), FormatTokens(r.Output.Value), think, "~" + FormatTokens(r.Text.Value), "~" + FormatTokens(r.ToolInput.Value)})
	}
	b.WriteString(table(data, o.width()))
}

var keyTitles = map[string]string{analyze.Unknown: "(not recorded)"}

func renderOutputMix(b *strings.Builder, om *OutputMix, o Options) {
	b.WriteString("\n" + heading("Output by kind (Claude Code history)"))
	t := om.Total
	b.WriteString(wrap(fmt.Sprintf("%s output tokens (measured): thinking %s (%s), visible text ~%s, tool input ~%s.",
		FormatTokens(t.Output.Value), FormatTokens(t.Thinking.Value), t.Thinking.Label, FormatTokens(t.Text.Value), FormatTokens(t.ToolInput.Value)), o.width(), "  ", "  ") + "\n")
	b.WriteString("\n")
	outputTable(b, "Phase", om.ByPhase, phaseTitles, o)
	b.WriteString("\n")
	outputTable(b, "Model", om.ByModel, keyTitles, o)
	b.WriteString("\n")
	outputTable(b, "Effort", om.ByEffort, keyTitles, o)
	b.WriteString("\n")
	outputTable(b, "Claude Code", om.ByVersion, keyTitles, o)
	notes := []string{"Output is measured. Thinking is measured where the log has a count; ~ marks estimates. Text and tool input are estimated by splitting the rest of the output by their byte sizes."}
	for _, r := range om.Ratios {
		src := "the prior, too few requests to calibrate"
		if r.Calibrated {
			src = fmt.Sprintf("calibrated on %d requests", r.Requests)
		}
		notes = append(notes, fmt.Sprintf("Bytes per token for %s: %.2f (%s).", r.Family, r.Ratio, src))
	}
	if t.Unsplit.Value > 0 {
		notes = append(notes, fmt.Sprintf("* includes %s output tokens from requests read before Trove recorded sizes; they count in Output only, and - means none of the row could be split.", FormatTokens(t.Unsplit.Value)))
	}
	if om.Carried.N > 0 {
		notes = append(notes, fmt.Sprintf("Earlier thinking carried in context: up to %s input tokens over %d requests on models that keep it, %s of their input (estimated upper bound).",
			FormatTokens(om.Carried.Value), om.Carried.N, FormatPercent(om.CarriedShare.Value)))
	}
	for _, n := range notes {
		b.WriteString(wrap(n, o.width(), "  ", "  ") + "\n")
	}
}
