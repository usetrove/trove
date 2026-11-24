package analyze

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The two ledgers below run on agent history (Claude Code), not on gateway requests. They take
// numbers and codes only and return numbers and codes only: counts, token sizes, cause codes and
// timestamps. See docs/analyze.md, "History ledgers".
//
// Facts they rely on, checked 2026-11-23:
//   - code.claude.com/docs/en/prompt-caching: the main conversation gets a one-hour cache on a
//     subscription within plan usage and five minutes otherwise; subagents, workflows and
//     compaction get five minutes. Switching models, changing effort (except on Fable 5.1 and later,
//     Opus 5.5, Sonnet 5.5 and Haiku 5.5), turning on fast mode, compacting and upgrading Claude
//     Code invalidate the cache; a subagent's first request does not read the parent's cache.
//   - platform.claude.com/docs/en/build-with-claude/preserved-thinking: on models with preserved
//     thinking, earlier thinking blocks stay in context and are billed as input.

// Rebuild thresholds. A request is a rebuild when its cache write is more than RebuildFactor times
// the content that is new since the agent's previous request, and more than RebuildMinTokens. Below
// that, a write is ordinary growth: the new turn's own content being cached.
const (
	RebuildFactor    = 3
	RebuildMinTokens = 20_000
)

// Cause codes, in the order they are tried: the first that matches wins.
const (
	CauseSessionStart = "session_start" // the main conversation's first request: a build, not a rebuild
	CauseIdle         = "idle_expired"  // gap since the agent's previous request longer than the TTL in effect
	CauseModel        = "model_changed"
	CauseSubagent     = "subagent_start" // a subagent's first request never reads the parent's cache
	CauseCompaction   = "compaction"
	CauseVersion      = "version_changed"
	CauseEffort       = "effort_changed"    // only on models whose cache is per effort level
	CauseFastMode     = "fast_mode_changed" // fast and standard speed keep separate caches
	CauseUnexplained  = "unexplained"       // none of the above: a candidate bug
)

// RebuildCauses lists every cause code in classification order.
var RebuildCauses = []string{CauseSessionStart, CauseIdle, CauseModel, CauseSubagent, CauseCompaction,
	CauseVersion, CauseEffort, CauseFastMode, CauseUnexplained}

// Cache lifetimes, in milliseconds.
const (
	ttl5m = 5 * 60 * 1000
	ttl1h = 60 * 60 * 1000
)

// LedgerRequest is one history request as the ledgers see it.
type LedgerRequest struct {
	SessionKey string
	AgentKey   string // "" for the main conversation
	Sidechain  bool
	TS         int64 // unix ms
	Turn       int
	Model      string
	Plan       string // api_key | subscription | unknown (estimated by the caller)

	AgentVersion string
	Effort       string // the turn's effort when known, else the setting
	ServiceTier  string

	Phase     string // logs.Phase*; "" when the parser did not record it
	Compacted bool

	Input, CacheWrite5m, CacheWrite1h, CacheWriteNA, CacheRead, Output int64
	Thinking                                                           *int64 // nil = not in the log

	TextBytes      int64
	ToolInputBytes int64
	HasSizes       bool // TextBytes was recorded (parser version 3 or later)
}

func (r *LedgerRequest) cacheWrite() int64 { return r.CacheWrite5m + r.CacheWrite1h + r.CacheWriteNA }

func (r *LedgerRequest) context() int64 { return r.Input + r.cacheWrite() + r.CacheRead }

func (r *LedgerRequest) main() bool { return r.AgentKey == "" && !r.Sidechain }

// sortLedger orders requests by session, agent, time and turn, so every ledger is deterministic.
func sortLedger(reqs []LedgerRequest) []LedgerRequest {
	out := append([]LedgerRequest(nil), reqs...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.SessionKey != b.SessionKey {
			return a.SessionKey < b.SessionKey
		}
		if a.AgentKey != b.AgentKey {
			return a.AgentKey < b.AgentKey
		}
		if a.TS != b.TS {
			return a.TS < b.TS
		}
		return a.Turn < b.Turn
	})
	return out
}

// RebuildEntry is one request whose cache write was a build or a rebuild.
type RebuildEntry struct {
	TS           int64  `json:"ts"`
	Cause        string `json:"cause"`
	CacheWrite   int64  `json:"cache_write_tokens"` // measured (from the log)
	NewContent   int64  `json:"new_content_tokens"` // measured: context growth since the agent's previous request
	GapMS        int64  `json:"gap_ms"`             // since the agent's previous request; 0 for a first request
	TTLMS        int64  `json:"ttl_ms"`             // the TTL the idle check used
	TTLEstimated bool   `json:"ttl_estimated"`      // TTL from the documented default, not seen in the log
	Model        string `json:"model"`
}

// RebuildLedger returns one entry per request whose cache write was a rebuild, plus each main
// conversation's first request (CauseSessionStart) when it wrote more than RebuildMinTokens.
func RebuildLedger(reqs []LedgerRequest) []RebuildEntry {
	sorted := sortLedger(reqs)
	var out []RebuildEntry
	var prev *LedgerRequest
	var ttl int64 // TTL seen in the agent's own writes so far; 0 = not seen
	for i := range sorted {
		r := &sorted[i]
		if prev != nil && (prev.SessionKey != r.SessionKey || prev.AgentKey != r.AgentKey) {
			prev, ttl = nil, 0
		}
		cw := r.cacheWrite()
		e := RebuildEntry{TS: r.TS, CacheWrite: cw, Model: r.Model}
		if prev == nil {
			if cw > RebuildMinTokens {
				e.Cause = CauseSubagent
				if r.main() {
					e.Cause = CauseSessionStart
				}
				out = append(out, e)
			}
		} else {
			e.NewContent = max(0, r.context()-prev.context())
			e.GapMS = r.TS - prev.TS
			e.TTLMS, e.TTLEstimated = ttl, ttl == 0
			if ttl == 0 {
				e.TTLMS = defaultTTL(prev)
			}
			if cw > RebuildMinTokens && cw > RebuildFactor*e.NewContent {
				e.Cause = classify(prev, r, e.GapMS, e.TTLMS)
				out = append(out, e)
			}
		}
		switch {
		case r.CacheWrite1h > 0:
			ttl = ttl1h
		case r.CacheWrite5m > 0:
			ttl = ttl5m
		}
		prev = r
	}
	return out
}

// defaultTTL is the documented default for a request like r when the log has not shown one.
func defaultTTL(r *LedgerRequest) int64 {
	if r.main() && r.Plan == "subscription" {
		return ttl1h
	}
	return ttl5m
}

func classify(prev, r *LedgerRequest, gap, ttl int64) string {
	switch {
	case gap > ttl:
		return CauseIdle
	case prev.Model != r.Model:
		return CauseModel
	case r.Compacted || r.Phase == "compact":
		return CauseCompaction
	case prev.AgentVersion != "" && r.AgentVersion != "" && prev.AgentVersion != r.AgentVersion:
		return CauseVersion
	case prev.Effort != "" && r.Effort != "" && prev.Effort != r.Effort && !effortKeepsCache(r.Model):
		return CauseEffort
	case isFast(prev.ServiceTier) != isFast(r.ServiceTier):
		return CauseFastMode
	}
	return CauseUnexplained
}

func isFast(tier string) bool { return strings.EqualFold(tier, "fast") }

var modelRe = regexp.MustCompile(`^(?:claude-)?([a-z]+)-([0-9]+)(?:-([0-9]{1,2}))?(?:-[0-9]{8})?(?:\[1m\])?$`)

// modelVersion splits a model id such as "claude-opus-5-5" or "claude-fable-5-2-20261101" into
// its family and version. ok is false for anything else.
func modelVersion(model string) (family string, major, minor int, ok bool) {
	m := modelRe.FindStringSubmatch(strings.ToLower(model))
	if m == nil {
		return "", 0, 0, false
	}
	major, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		minor, _ = strconv.Atoi(m[3])
	}
	return m[1], major, minor, true
}

func atLeast(major, minor, wantMajor, wantMinor int) bool {
	return major > wantMajor || (major == wantMajor && minor >= wantMinor)
}

// effortKeepsCache: models whose cache survives an effort change (code.claude.com/docs/en/prompt-caching,
// 2026-11-23): Fable 5.1 and later, Opus 5.5, Sonnet 5.5 and Haiku 5.5.
func effortKeepsCache(model string) bool {
	f, major, minor, ok := modelVersion(model)
	if !ok {
		return false
	}
	switch f {
	case "fable":
		return atLeast(major, minor, 5, 1)
	case "opus", "sonnet", "haiku":
		return major == 5 && minor == 5
	}
	return false
}

// preservesThinking: models that keep earlier thinking in context and bill it as input, as listed on
// platform.claude.com/docs/en/build-with-claude/preserved-thinking (2026-11-23): Fable and Mythos
// 5.1 and 5.2, Opus 5.5, Sonnet 5.5 and Haiku 5.5. Other models are not counted, even where another
// page suggests they keep thinking too; the carried figure stays an upper bound for the models it covers.
func preservesThinking(model string) bool {
	f, major, minor, ok := modelVersion(model)
	if !ok {
		return false
	}
	switch f {
	case "fable", "mythos":
		return major == 5 && (minor == 1 || minor == 2)
	case "opus", "sonnet", "haiku":
		return major == 5 && minor == 5
	}
	return false
}

// RebuildRow totals one cause over a window.
type RebuildRow struct {
	Cause    string  `json:"cause"`
	Count    int     `json:"count"`
	Tokens   int64   `json:"cache_write_tokens"` // measured
	Share    float64 `json:"share"`              // of all rebuild tokens in the window (session starts excluded)
	TTLGuess int     `json:"ttl_estimated"`      // entries whose idle check used a documented default TTL
}

// RebuildSummary totals the ledger for entries with from <= TS < to.
type RebuildSummary struct {
	Rows             []RebuildRow `json:"by_cause"` // causes with at least one entry, in classification order
	RebuildCount     int          `json:"rebuilds"` // excludes session starts
	RebuildTokens    int64        `json:"rebuild_tokens"`
	CacheWriteTokens int64        `json:"cache_write_tokens"` // all cache writes in the window, for scale
}

// SummarizeRebuilds totals entries in [from, to). cacheWrites is the window's total cache-write tokens.
func SummarizeRebuilds(entries []RebuildEntry, from, to, cacheWrites int64) RebuildSummary {
	by := map[string]*RebuildRow{}
	s := RebuildSummary{CacheWriteTokens: cacheWrites}
	for _, e := range entries {
		if e.TS < from || e.TS >= to {
			continue
		}
		row := by[e.Cause]
		if row == nil {
			row = &RebuildRow{Cause: e.Cause}
			by[e.Cause] = row
		}
		row.Count++
		row.Tokens += e.CacheWrite
		if e.Cause == CauseIdle && e.TTLEstimated {
			row.TTLGuess++
		}
		if e.Cause != CauseSessionStart {
			s.RebuildCount++
			s.RebuildTokens += e.CacheWrite
		}
	}
	for _, c := range RebuildCauses {
		row := by[c]
		if row == nil {
			continue
		}
		if c != CauseSessionStart && s.RebuildTokens > 0 {
			row.Share = ratio(row.Tokens, s.RebuildTokens)
		}
		s.Rows = append(s.Rows, *row)
	}
	return s
}

func ratio(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(int64(float64(a)/float64(b)*10000+0.5)) / 10000
}
