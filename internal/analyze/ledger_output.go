package analyze

import (
	"math"
	"sort"
)

// Output split. Each request's output is split into thinking, visible text and tool input:
//   - thinking is measured when the log carries thinking_tokens (it is inside output_tokens);
//   - otherwise it is output minus an estimate of the visible part, and labelled estimated;
//   - the rest is split between text and tool input by their byte sizes, always estimated.
// Bytes are turned into tokens with a ratio calibrated per model family from requests where thinking
// was measured (their non-thinking output against their text and tool-input bytes), or the prior
// DefaultBytesPerToken when there are too few.

// Calibration needs at least this many requests with measured thinking and visible bytes, and this
// many non-thinking output tokens between them, before its ratio replaces the prior.
const (
	minCalibrationRequests = 20
	minCalibrationTokens   = 2_000
)

// OutputEntry is one request's output split.
type OutputEntry struct {
	TS      int64
	Phase   string
	Model   string
	Effort  string
	Version string

	Output           int64 // measured
	Thinking         int64
	ThinkingMeasured bool
	Text             int64 // estimated
	ToolInput        int64 // estimated
	Unsplit          bool  // no thinking count and no sizes: the output cannot be split
	Context          int64 // measured: all input tokens of the request

	// Carried is the earlier thinking of the same agent still in context at this request: an
	// estimated upper bound, counted only on models with preserved thinking (CarryCounted).
	Carried      int64
	CarryCounted bool
}

// BytesPerToken is the ratio used for one model family.
type BytesPerToken struct {
	Family     string  `json:"family"`
	Ratio      float64 `json:"bytes_per_token"`
	Calibrated bool    `json:"calibrated"` // from this history; false = the prior
	Requests   int     `json:"requests"`   // requests the calibration used
}

func ledgerFamily(model string) string {
	if f, _, _, ok := modelVersion(model); ok {
		return f
	}
	return model
}

// calibrate returns the bytes-per-token ratio per model family.
func calibrate(reqs []LedgerRequest) map[string]BytesPerToken {
	type acc struct {
		bytes, tokens int64
		n             int
	}
	sums := map[string]*acc{}
	for i := range reqs {
		r := &reqs[i]
		if r.Thinking == nil || !r.HasSizes {
			continue
		}
		rest, b := r.Output-*r.Thinking, r.TextBytes+r.ToolInputBytes
		if rest <= 0 || b <= 0 {
			continue
		}
		f := ledgerFamily(r.Model)
		a := sums[f]
		if a == nil {
			a = &acc{}
			sums[f] = a
		}
		a.bytes += b
		a.tokens += rest
		a.n++
	}
	out := map[string]BytesPerToken{}
	for f, a := range sums {
		if a.n >= minCalibrationRequests && a.tokens >= minCalibrationTokens {
			out[f] = BytesPerToken{Family: f, Ratio: math.Round(float64(a.bytes)/float64(a.tokens)*100) / 100, Calibrated: true, Requests: a.n}
		}
	}
	return out
}

// OutputLedger splits every request's output and tracks thinking carried in context. It returns the
// entries and the ratios it used.
func OutputLedger(reqs []LedgerRequest) ([]OutputEntry, []BytesPerToken) {
	sorted := sortLedger(reqs)
	cal := calibrate(sorted)
	used := map[string]BytesPerToken{}
	ratioFor := func(model string) float64 {
		f := ledgerFamily(model)
		b, ok := cal[f]
		if !ok {
			b = BytesPerToken{Family: f, Ratio: DefaultBytesPerToken}
		}
		used[f] = b
		return b.Ratio
	}

	out := make([]OutputEntry, 0, len(sorted))
	var prev *LedgerRequest
	var carry int64
	for i := range sorted {
		r := &sorted[i]
		if prev == nil || prev.SessionKey != r.SessionKey || prev.AgentKey != r.AgentKey ||
			prev.Model != r.Model || r.Compacted || r.Phase == "compact" {
			carry = 0 // a new agent, a model switch or a compaction leaves no earlier thinking in context
		}
		e := OutputEntry{TS: r.TS, Phase: r.Phase, Model: r.Model, Effort: r.Effort, Version: r.AgentVersion,
			Output: r.Output, Context: r.context()}
		visBytes := r.TextBytes + r.ToolInputBytes
		switch {
		case r.Thinking != nil:
			e.Thinking, e.ThinkingMeasured = min(*r.Thinking, r.Output), true
		case r.HasSizes:
			visible := int64(math.Round(float64(visBytes) / ratioFor(r.Model)))
			e.Thinking = max(0, r.Output-visible)
		default:
			e.Unsplit = true
		}
		if !e.Unsplit {
			rest := r.Output - e.Thinking
			switch {
			case !r.HasSizes || visBytes == 0:
				e.Text = rest // nothing to split by: count it as visible text
			default:
				e.ToolInput = int64(math.Round(float64(rest) * float64(r.ToolInputBytes) / float64(visBytes)))
				e.Text = rest - e.ToolInput
			}
		}
		if preservesThinking(r.Model) {
			e.Carried, e.CarryCounted = carry, true
			carry += e.Thinking
		}
		out = append(out, e)
		prev = r
	}
	ratios := make([]BytesPerToken, 0, len(used))
	for _, b := range used {
		ratios = append(ratios, b)
	}
	sort.Slice(ratios, func(i, j int) bool { return ratios[i].Family < ratios[j].Family })
	return out, ratios
}

// OutputBucket totals output for one key. Output is measured; Thinking is measured except for
// ThinkingEstimated of it; Text and ToolInput are always estimated; Unsplit is output from requests
// that could not be split.
type OutputBucket struct {
	Key               string `json:"key"`
	Requests          int    `json:"requests"`
	Output            int64  `json:"output_tokens"`
	Thinking          int64  `json:"thinking_tokens"`
	ThinkingEstimated int64  `json:"thinking_estimated_tokens"`
	Text              int64  `json:"text_tokens"`
	ToolInput         int64  `json:"tool_input_tokens"`
	Unsplit           int64  `json:"unsplit_tokens"`
}

func (b *OutputBucket) add(e *OutputEntry) {
	b.Requests++
	b.Output += e.Output
	if e.Unsplit {
		b.Unsplit += e.Output
		return
	}
	b.Thinking += e.Thinking
	if !e.ThinkingMeasured {
		b.ThinkingEstimated += e.Thinking
	}
	b.Text += e.Text
	b.ToolInput += e.ToolInput
}

// OutputSummary totals the output ledger over a window.
type OutputSummary struct {
	Total     OutputBucket   `json:"total"`
	ByPhase   []OutputBucket `json:"by_phase"`
	ByModel   []OutputBucket `json:"by_model"`
	ByEffort  []OutputBucket `json:"by_effort"`
	ByVersion []OutputBucket `json:"by_version"`

	// Earlier thinking still in context, summed over the requests that carried it: an estimated upper
	// bound of the input tokens it took. Context is all input tokens of the requests counted (measured).
	CarriedTokens   int64 `json:"carried_thinking_tokens"`
	CarriedRequests int   `json:"carried_requests"` // requests on models with preserved thinking
	CarriedContext  int64 `json:"carried_context_tokens"`

	Ratios []BytesPerToken `json:"bytes_per_token"`
}

// Unknown is the key for requests whose phase, effort or version the log did not give.
const Unknown = "unknown"

// SummarizeOutput totals entries with from <= TS < to.
func SummarizeOutput(entries []OutputEntry, ratios []BytesPerToken, from, to int64) OutputSummary {
	s := OutputSummary{Total: OutputBucket{Key: "total"}, Ratios: ratios}
	phase, model, effort, version := map[string]*OutputBucket{}, map[string]*OutputBucket{}, map[string]*OutputBucket{}, map[string]*OutputBucket{}
	put := func(m map[string]*OutputBucket, k string, e *OutputEntry) {
		if k == "" {
			k = Unknown
		}
		b := m[k]
		if b == nil {
			b = &OutputBucket{Key: k}
			m[k] = b
		}
		b.add(e)
	}
	for i := range entries {
		e := &entries[i]
		if e.TS < from || e.TS >= to {
			continue
		}
		s.Total.add(e)
		put(phase, e.Phase, e)
		put(model, e.Model, e)
		put(effort, e.Effort, e)
		put(version, e.Version, e)
		if e.CarryCounted {
			s.CarriedRequests++
			s.CarriedTokens += e.Carried
			s.CarriedContext += e.Context
		}
	}
	s.ByPhase, s.ByModel, s.ByEffort, s.ByVersion = buckets(phase), buckets(model), buckets(effort), buckets(version)
	return s
}

// buckets returns m's buckets, largest output first, then by key.
func buckets(m map[string]*OutputBucket) []OutputBucket {
	out := make([]OutputBucket, 0, len(m))
	for _, b := range m {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Output != out[j].Output {
			return out[i].Output > out[j].Output
		}
		return out[i].Key < out[j].Key
	})
	return out
}
