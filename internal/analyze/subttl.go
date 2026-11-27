package analyze

// Subagent cache lifetime: what a one-hour lifetime would have changed for the requests made outside the
// main conversation (subagents, workflows, compaction, session titles). Numbers only, per model.
//
// Facts, read on 2026-11-24 (docs/techniques.md, "What was verified", S10):
//   - code.claude.com/docs/en/prompt-caching: `subagentPromptCacheTtl` and
//     `CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL` choose the lifetime of "the requests Claude Code makes
//     outside that conversation, such as subagents, workflows, in-process teammates, forks, compaction,
//     and session titles", "5m" or "1h", from v2.1.242. Their default is five minutes (one hour only for
//     server-controlled helper requests on a subscription).
//   - platform.claude.com/docs/en/build-with-claude/prompt-caching: a five-minute write costs 1.25x the
//     base input price, a one-hour write 2x, a read 0.1x (0.05x or 0.025x on some newer models).

// gapLo and gapHi bound the pauses only a one-hour entry survives: more than five minutes, at most sixty.
const (
	gapLo = ttl5m
	gapHi = ttl1h
)

// SubTTLTokens are one model's subagent tokens that decide the lifetime. All are measured from the log.
type SubTTLTokens struct {
	Requests int
	// Write5m, Write1h and WriteNA are the cache writes at each lifetime (NA: the log gave no split).
	Write5m, Write1h, WriteNA int64
	// GapRebuilds and GapRebuildTokens: rebuild-ledger entries caused by a 5-to-60-minute pause while the
	// agent was on the five-minute lifetime. A one-hour entry would have been read instead.
	GapRebuilds      int
	GapRebuildTokens int64
	// GapReads and GapReadTokens: requests that read the cache after a 5-to-60-minute pause, which only a
	// one-hour entry serves. A five-minute entry would have been rewritten.
	GapReads      int
	GapReadTokens int64
}

// SubTTL is the subagent bucket's history, per model, for one window.
type SubTTL struct {
	Requests int
	Sessions int
	// APIKey is how many of the requests were on an API key.
	APIKey  int
	ByModel map[string]*SubTTLTokens
}

// Total adds up every model.
func (s SubTTL) Total() SubTTLTokens {
	var t SubTTLTokens
	for _, m := range s.ByModel {
		t.Requests += m.Requests
		t.Write5m, t.Write1h, t.WriteNA = t.Write5m+m.Write5m, t.Write1h+m.Write1h, t.WriteNA+m.WriteNA
		t.GapRebuilds, t.GapRebuildTokens = t.GapRebuilds+m.GapRebuilds, t.GapRebuildTokens+m.GapRebuildTokens
		t.GapReads, t.GapReadTokens = t.GapReads+m.GapReads, t.GapReadTokens+m.GapReadTokens
	}
	return t
}

// SubagentTTL reads the subagent requests in reqs: their writes by lifetime, the rebuilds a one-hour
// lifetime would have avoided (from RebuildLedger), and the reads after a pause a one-hour entry served.
// Main-conversation requests are ignored. Requests with no cache activity are not counted.
func SubagentTTL(reqs []LedgerRequest) SubTTL {
	out := SubTTL{ByModel: map[string]*SubTTLTokens{}}
	of := func(model string) *SubTTLTokens {
		m := out.ByModel[model]
		if m == nil {
			m = &SubTTLTokens{}
			out.ByModel[model] = m
		}
		return m
	}
	sessions := map[string]bool{}
	sorted := sortLedger(reqs)
	var prev *LedgerRequest
	for i := range sorted {
		r := &sorted[i]
		if prev != nil && (prev.SessionKey != r.SessionKey || prev.AgentKey != r.AgentKey) {
			prev = nil
		}
		if !r.main() && r.cacheWrite()+r.CacheRead > 0 {
			m := of(r.Model)
			m.Requests++
			out.Requests++
			if r.Plan == "api_key" {
				out.APIKey++
			}
			sessions[r.SessionKey] = true
			m.Write5m, m.Write1h, m.WriteNA = m.Write5m+r.CacheWrite5m, m.Write1h+r.CacheWrite1h, m.WriteNA+r.CacheWriteNA
			if prev != nil && r.CacheRead > 0 {
				if gap := r.TS - prev.TS; gap > gapLo && gap <= gapHi {
					m.GapReads++
					m.GapReadTokens += r.CacheRead
				}
			}
		}
		prev = r
	}
	for _, e := range RebuildLedger(reqs) {
		if e.Subagent && e.Cause == CauseIdle && e.TTLMS == ttl5m && e.GapMS > gapLo && e.GapMS <= gapHi {
			m := of(e.Model)
			m.GapRebuilds++
			m.GapRebuildTokens += e.CacheWrite - e.Write1h
		}
	}
	out.Sessions = len(sessions)
	return out
}
