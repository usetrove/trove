package measure

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/store"
)

// Learned bytes per token (plan item 1.6).
//
// Trove turns byte sizes into token estimates in several places (hook savings, shadow estimates). The
// starting ratio is 3.5. The session logs say what the provider really counted, so for each model family
// this learns the ratio from pairs of consecutive requests: the tool results one request produced are the
// main new content the next request has to read. The learned ratio replaces 3.5 in hook estimates and is
// shown with its sample size. Every number it produces is an estimate.

const (
	// MinRatioSamples is how many clean pairs a family needs before its ratio is used. Below it the
	// default ratio stands, because a ratio from a handful of pairs is noise.
	MinRatioSamples = 20
	// RatioWindow is how much history is read.
	RatioWindow = 30 * 24 * time.Hour

	minPairBytes    = 2000 // smaller tool results are dominated by the message framing around them
	minRatio        = 1.5  // outside this range something other than text was in the prompt (images, a rebuilt prefix)
	maxRatio        = 8.0
	cacheContinuity = 0.9 // the next request must read back at least this much of the previous prompt from cache
)

// BytesRatio is the learned ratio for one model family.
type BytesRatio struct {
	BytesPerToken float64 `json:"r"`
	// Samples is the number of request pairs the ratio rests on.
	Samples int `json:"n"`
}

// ModelFamily groups models that share a tokenizer: the first two dash-separated parts of the lower-cased
// model name, without any "@provider" suffix ("claude-sonnet-5-5" -> "claude-sonnet", "gpt-5.1-codex" ->
// "gpt-5.1"). An empty name has no family.
func ModelFamily(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.IndexByte(m, '@'); i >= 0 {
		m = m[:i]
	}
	parts := strings.SplitN(m, "-", 3)
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, "-")
}

type pairTotals struct {
	bytes, tokens int64
	n             int
}

type histReq struct {
	session, model        string
	sidechain             bool
	input, write, read, o int64
	resultBytes           int64
}

// LearnBytesPerToken returns the learned ratio for each model family with at least MinRatioSamples clean
// pairs in the last RatioWindow. A pair is two consecutive requests of one session, the same model and
// the same agent (main or subagent), where the cache stayed warm and the earlier request's tool results
// were big enough to measure. The new tokens the later request had to read are its uncached input and
// cache writes less the earlier request's output (which joined the context too). Families whose ratio falls
// outside a plausible range are left out. The result is a pure function of the stored rows.
func LearnBytesPerToken(ctx context.Context, db *store.DB, now time.Time) (map[string]BytesRatio, error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key, model, sidechain, input_tokens,
			cache_write_5m + cache_write_1h + cache_write_other, cache_read, output_tokens, tool_result_bytes
		FROM history_requests WHERE ts >= ? AND ts <= ? AND model <> ''
		ORDER BY session_key, ts, request_key`, now.Add(-RatioWindow).UnixMilli(), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	totals := map[string]*pairTotals{}
	var prev histReq
	havePrev := false
	for rows.Next() {
		var cur histReq
		var side int
		if err := rows.Scan(&cur.session, &cur.model, &side, &cur.input, &cur.write, &cur.read, &cur.o, &cur.resultBytes); err != nil {
			return nil, err
		}
		cur.sidechain = side != 0
		if havePrev && prev.session == cur.session && prev.model == cur.model && prev.sidechain == cur.sidechain {
			if b, tok, ok := cleanPair(prev, cur); ok {
				fam := ModelFamily(cur.model)
				t := totals[fam]
				if t == nil {
					t = &pairTotals{}
					totals[fam] = t
				}
				t.bytes += b
				t.tokens += tok
				t.n++
			}
		}
		prev, havePrev = cur, true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string]BytesRatio{}
	for fam, t := range totals {
		if fam == "" || t.n < MinRatioSamples || t.tokens <= 0 {
			continue
		}
		r := float64(t.bytes) / float64(t.tokens)
		if r < minRatio || r > maxRatio {
			continue
		}
		out[fam] = BytesRatio{BytesPerToken: math.Round(r*100) / 100, Samples: t.n}
	}
	return out, nil
}

// cleanPair returns the bytes the earlier request's tools produced and the tokens the later request added
// for them, or false when the pair cannot be used.
func cleanPair(prev, cur histReq) (bytes, tokens int64, ok bool) {
	if prev.resultBytes < minPairBytes {
		return 0, 0, false
	}
	prevPrompt := prev.input + prev.write + prev.read
	if prevPrompt <= 0 || float64(cur.read) < cacheContinuity*float64(prevPrompt) {
		return 0, 0, false // the cache went cold, so the new tokens include a rebuilt prefix
	}
	tokens = cur.input + cur.write - prev.o
	if tokens <= 0 {
		return 0, 0, false
	}
	return prev.resultBytes, tokens, true
}
