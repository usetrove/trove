package suggest

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/usetrove/trove/internal/pricing"
)

// KindCacheTTL is the suggestion about the prompt cache lifetime.
const KindCacheTTL = "cache_ttl"

// cache_ttl: would the other prompt cache lifetime have been cheaper for this
// person's own sessions?
//
// Facts, from official pages read on 2026-11-13 (docs/techniques.md, "What was
// verified"):
//   - A cache entry lives 5 minutes by default; a one-hour lifetime is requested
//     per entry. A one-hour write costs 2x the base input price and a five-minute
//     write 1.25x; a cache read costs a tenth or less of the base price
//     (platform.claude.com/docs/en/build-with-claude/prompt-caching).
//   - Claude Code lets you pick the lifetime for the main conversation
//     (`promptCacheTtl`) and for subagents and its own helper requests
//     (`subagentPromptCacheTtl`), each "5m" or "1h", from v2.1.242
//     (code.claude.com/docs/en/settings-reference).
//
// What it reads: Claude Code's own history, which records how many tokens each
// request wrote at each lifetime. Token counts, times and model names only.
//
// How it decides. For each request it takes the gap since the previous request
// in the same session (main conversation and subagents counted separately).
//   - A gap of 5 minutes or less is served by either lifetime.
//   - A gap of more than 5 and up to 60 minutes is served only by a one-hour
//     entry. Those requests are what the longer lifetime pays for.
//   - A longer gap is served by neither.
//
// If the person is on one hour, the one-hour premium is the extra write price
// over the five-minute price on every token written at one hour. What the premium
// bought is the rewrite it avoided: the tokens read after a 5-to-60-minute gap,
// which a five-minute entry would have lost, priced at the five-minute write price
// minus the read price. If the person is on five minutes, the same reasoning runs
// the other way: the cached prompt that had to be rewritten after a 5-to-60-minute
// gap against what a one-hour lifetime would have cost on every write.
//
// It speaks up only when the other lifetime is clearly cheaper (see the
// thresholds). It assumes a lost entry is rewritten in full, which makes it an
// estimate. A person's pauses can change, so the figure is for the past window,
// not a forecast.
const (
	// minTTLRequests is how many requests with cache activity a bucket needs.
	minTTLRequests = 100
	// minTTLUSD is the smallest difference worth mentioning, at list prices.
	minTTLUSD = 1.0
	// ttlMargin: the other lifetime must cost at least this much less.
	ttlMargin = 1.25
	// maxUnsplitShare: if more than this share of written tokens has no lifetime
	// recorded, the history cannot say which lifetime was used.
	maxUnsplitShare = 0.2
	// confidentRequests is the size at which the confidence is medium.
	confidentRequests = 400
)

type ttlRow struct {
	session           string
	sidechain         bool
	ts                int64
	model, plan       string
	w5, w1, wo, reads int64
}

type ttlBucket struct {
	reqs           int
	sessions       map[string]bool
	w5, w1, wOther int64
	// premium1h: what one-hour writes cost over five-minute writes.
	premium1h float64
	// avoided1h: the rewrite cost the one-hour entries avoided.
	avoided1h float64
	// premiumIf1h: what the five-minute writes would have cost extra at one hour.
	premiumIf1h float64
	// savedIf1h: the rewrite cost a one-hour lifetime would have avoided.
	savedIf1h float64
	apiKey    int // requests on an API key
	otherPlan int // requests on a subscription or an unknown plan
	unpriced  int
}

// cacheTTL builds the cache-lifetime suggestions for requests with from <= ts < to.
func cacheTTL(ctx context.Context, db Querier, tbl *pricing.Table, from, to time.Time) ([]Suggestion, error) {
	buckets, err := ttlBuckets(ctx, db, tbl, from, to)
	if err != nil || buckets == nil {
		return nil, err
	}
	var out []Suggestion
	for _, side := range []bool{false, true} {
		if s, ok := buckets[side].suggestion(side); ok {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, nil
}

// ttlBuckets reads the history and totals each bucket (main conversation, subagents).
// It returns nil, nil when there is no history table yet.
func ttlBuckets(ctx context.Context, db Querier, tbl *pricing.Table, from, to time.Time) (map[bool]*ttlBucket, error) {
	rows, err := db.QueryContext(ctx, `SELECT session_key, sidechain, ts, model, plan,
		cache_write_5m, cache_write_1h, cache_write_other, cache_read
		FROM history_requests WHERE source = 'claude_code' AND ts >= ? AND ts < ?
		AND (cache_write_5m + cache_write_1h + cache_write_other + cache_read) > 0
		ORDER BY session_key, sidechain, ts`, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, nil // no history table yet: nothing to say
	}
	defer func() { _ = rows.Close() }()

	buckets := map[bool]*ttlBucket{false: newTTLBucket(), true: newTTLBucket()}
	type price struct{ w5, w1, rd float64 }
	memo := map[string]*price{}
	priceOf := func(model string, ts int64) *price {
		k := model + "|" + strconv.FormatInt(ts/86400000, 10)
		if p, ok := memo[k]; ok {
			return p
		}
		var p *price
		if e, _ := tbl.Lookup(pricing.Anthropic, model, time.UnixMilli(ts)); e != nil && e.Standard.CacheRead != nil {
			w5, ok5 := e.Standard.CacheWrite["5m"]
			w1, ok1 := e.Standard.CacheWrite["1h"]
			if ok5 && ok1 {
				f5, e5 := strconv.ParseFloat(w5.String(), 64)
				f1, e1 := strconv.ParseFloat(w1.String(), 64)
				fr, er := strconv.ParseFloat(e.Standard.CacheRead.String(), 64)
				if e5 == nil && e1 == nil && er == nil {
					p = &price{w5: f5 / 1e6, w1: f1 / 1e6, rd: fr / 1e6}
				}
			}
		}
		memo[k] = p
		return p
	}

	var prev *ttlRow
	for rows.Next() {
		var r ttlRow
		var side int
		if err := rows.Scan(&r.session, &side, &r.ts, &r.model, &r.plan, &r.w5, &r.w1, &r.wo, &r.reads); err != nil {
			return nil, err
		}
		r.sidechain = side != 0
		b := buckets[r.sidechain]
		b.reqs++
		b.sessions[r.session] = true
		b.w5, b.w1, b.wOther = b.w5+r.w5, b.w1+r.w1, b.wOther+r.wo
		if r.plan == "api_key" {
			b.apiKey++
		} else {
			b.otherPlan++
		}
		p := priceOf(r.model, r.ts)
		if p == nil {
			b.unpriced++
		} else {
			b.premium1h += float64(r.w1) * (p.w1 - p.w5)
			b.premiumIf1h += float64(r.w5) * (p.w1 - p.w5)
			if prev != nil && prev.session == r.session && prev.sidechain == r.sidechain {
				gap := float64(r.ts-prev.ts) / 60000.0
				if gap > 5 && gap <= 60 {
					b.avoided1h += float64(r.reads) * (p.w5 - p.rd)
					cachedNow := r.reads + r.w5 + r.w1 + r.wo
					cachedBefore := prev.reads + prev.w5 + prev.w1 + prev.wo
					if missed := min(cachedBefore, cachedNow) - r.reads; missed > 0 {
						b.savedIf1h += float64(missed) * (p.w5 - p.rd)
					}
				}
			}
		}
		cur := r
		prev = &cur
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return buckets, nil
}

func newTTLBucket() *ttlBucket { return &ttlBucket{sessions: map[string]bool{}} }

func (b *ttlBucket) suggestion(sidechain bool) (Suggestion, bool) {
	written := b.w5 + b.w1 + b.wOther
	if b.reqs < minTTLRequests || written == 0 || b.unpriced > b.reqs/4 {
		return Suggestion{}, false
	}
	if float64(b.wOther)/float64(written) > maxUnsplitShare {
		return Suggestion{}, false // the history does not say which lifetime was used
	}
	split := b.w5 + b.w1
	if split == 0 {
		return Suggestion{}, false
	}
	onHour := float64(b.w1)/float64(split) >= 0.5

	// gain is what the other lifetime would have saved, loss what it would have
	// cost. Switching from one hour to five minutes saves the one-hour premium and
	// loses the rewrites the premium was avoiding. Switching from five minutes to
	// one hour saves the rewrites and pays the premium on every write.
	var gain, loss float64
	to := "1h"
	if onHour {
		to, gain, loss = "5m", b.premium1h, b.avoided1h
	} else {
		gain, loss = b.savedIf1h, b.premiumIf1h
	}
	if !(gain > loss*ttlMargin && gain-loss >= minTTLUSD) {
		return Suggestion{}, false
	}
	net := gain - loss

	who, key := "your main conversation", "promptCacheTtl"
	if sidechain {
		who, key = "your subagents and Claude Code's helper requests", "subagentPromptCacheTtl"
	}
	cur := "5-minute"
	if onHour {
		cur = "1-hour"
	}
	want := map[string]string{"5m": "5-minute", "1h": "1-hour"}[to]

	s := Suggestion{
		Kind:       KindCacheTTL,
		Title:      fmt.Sprintf("Try a %s cache lifetime for %s", want, who),
		Label:      "estimated",
		Requests:   b.reqs,
		Confidence: "low",
	}
	if b.reqs >= confidentRequests && len(b.sessions) >= 20 {
		s.Confidence = "medium"
	}
	s.Detail = fmt.Sprintf("Across %d requests in %d sessions, %s mostly wrote its cache at the %s lifetime. "+
		"At list prices, the %s lifetime would have cost about $%.2f less over this period, counting the pauses of 5 to 60 minutes that only a one-hour entry survives. "+
		"To try it, set %q to %q in your Claude Code settings (version 2.1.242 or later; the setting is documented at code.claude.com/docs/en/settings-reference). "+
		"This assumes a lost cache entry is rewritten in full and that you pause the way you did in this period, so treat it as a rough guide and look again after a week.",
		b.reqs, len(b.sessions), who, cur, want, net, key, to)

	figure := net
	if b.apiKey == b.reqs && b.otherPlan == 0 {
		s.EstSavingsUSD = &figure
	} else {
		// A subscription or an unknown plan may not bill per token, so a dollar
		// figure could be wrong (CLAUDE.md rule 9). Say what it is, and no more.
		s.Scenario = fmt.Sprintf("At list prices the difference is about $%.2f over this period. Your history does not show that you pay per token, so that is a list-price equivalent, not money you would have saved.", net)
	}
	return s, true
}
