package analyze

import (
	"hash/fnv"
	"regexp"
	"strings"
	"time"
)

// Cache-miss reason codes.
const (
	ReasonVolatile   = "volatile_content"
	ReasonTools      = "tool_definitions_changed"
	ReasonTTL        = "ttl_expired"
	ReasonBelowMin   = "below_minimum"
	ReasonNoMarkers  = "no_cache_markers"
	ReasonModel      = "model_switched"
	ReasonUnknown    = "unknown"
	maxLinesPerSeg   = 4000
	maxLineSegments  = 16
	maxSnapSegments  = 10000
	snapshotsPerSess = 3
	maxVolatileLines = 5
)

// snapshot is what is kept of a request in memory: hashes and sizes only.
type snapshot struct {
	at        time.Time
	model     string
	markers   bool
	oneHour   bool
	hashes    []hash
	cum       []int64 // cumulative estimated tokens; cum[i] = tokens before segment i
	total     int64   // tokens the provider reported for the prompt
	cached    int64   // cache read + write the provider reported
	toolSeq   []hash
	lines     map[int][]lineInfo // line hashes for system-level segments, by segment index
	truncated bool
}

// lineInfo is one line of a system-level segment: a hash and a length, never the text.
type lineInfo struct {
	h uint64
	n int32
}

func lineHashes(text string) []lineInfo {
	var out []lineInfo
	for _, l := range strings.Split(text, "\n") {
		if len(out) >= maxLinesPerSeg {
			break
		}
		out = append(out, lineInfo{hashLine(l), int32(len(l) + 1)})
	}
	return out
}

func hashLine(l string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(strings.TrimSpace(l)))
	return h.Sum64()
}

// wantsLines says whether a segment's line hashes are worth keeping: system
// prompts, context files and the first messages, where a changing date or
// path would invalidate everything after it.
func wantsLines(s Segment) bool {
	return s.Category == CatSystem || s.Category == CatContextFile || (s.Msg >= 0 && s.Msg <= 1 && s.Kind == KindUserText)
}

func takeSnapshot(p *Parsed, e *Estimate, at time.Time, total, cached int64) *snapshot {
	n := min(len(p.Segments), maxSnapSegments)
	snap := &snapshot{at: at, model: p.Model, markers: p.CacheMarkers, oneHour: p.OneHourTTL, total: total, cached: cached,
		hashes: make([]hash, n), lines: map[int][]lineInfo{}, truncated: len(p.Segments) > n}
	off := e.cumulative()
	snap.cum = off[:n+1]
	kept := 0
	for i := 0; i < n; i++ {
		s := p.Segments[i]
		snap.hashes[i] = s.Hash
		if s.Kind == KindToolDef {
			snap.toolSeq = append(snap.toolSeq, s.Hash)
		}
		if kept < maxLineSegments && wantsLines(s) {
			snap.lines[i] = lineHashes(s.text)
			kept++
		}
	}
	return snap
}

// commonPrefix returns how many leading segments of cur match prev.
func commonPrefix(prev *snapshot, cur *Parsed) int {
	n := min(len(prev.hashes), len(cur.Segments))
	for i := 0; i < n; i++ {
		if prev.hashes[i] != cur.Segments[i].Hash {
			return i
		}
	}
	return n
}

// CacheAnalysis is the comparison of one request with the best-matching
// earlier request in the same session.
type CacheAnalysis struct {
	PrefixSegments int   // segments identical to the earlier request
	PrefixTokens   int64 // estimated tokens in them
	SharedTokens   int64 // estimated tokens of content also in the earlier request, wherever it now sits
	ExpectedRead   int64 // estimated tokens that should have been cache reads
	ReportedRead   int64 // cache-read tokens the provider reported
	MissedTokens   int64
	Miss           *Miss // nil when there was no significant miss
}

// Miss is one classified, significant cache miss. Only a category and a
// position are kept for changed content, never the text.
type Miss struct {
	Reason string
	Tokens int64
	Detail map[string]any
}

var (
	dateRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}|\d{1,2}/\d{1,2}/\d{2,4}|\b\d{1,2}:\d{2}(:\d{2})?\b|(?i)\b(mon|tues?|wed(nes)?|thu(rs)?|fri|sat(ur)?|sun)(day)?\b|(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.? \d{1,2}\b`)
	idRe   = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}|\b[0-9a-f]{12,}\b|\b[A-Za-z0-9]{20,}\b`)
	pathRe = regexp.MustCompile(`(?:[A-Za-z]:\\|/)[\w.\-]+(?:[\\/][\w.\-]+)+|~/[\w.\-]+`)
)

func spanCategory(text string) string {
	switch {
	case dateRe.MatchString(text):
		return "date"
	case idRe.MatchString(text):
		return "id"
	case pathRe.MatchString(text):
		return "path"
	}
	return "other"
}

// analyzeCache compares cur with the best earlier snapshot. total and read are
// the provider-reported prompt size and cache-read tokens; it returns nil when
// there is nothing to compare or the provider reported no usage.
func (a *Analyzer) analyzeCache(prev *snapshot, p *Parsed, e *Estimate, at time.Time, reported bool, read int64) *CacheAnalysis {
	if prev == nil || !reported {
		return nil
	}
	lcp := commonPrefix(prev, p)
	off := e.cumulative()
	ca := &CacheAnalysis{PrefixSegments: lcp, PrefixTokens: off[lcp], SharedTokens: sharedTokens(prev, p, e), ReportedRead: read}

	base := prev.total
	if p.Provider == "anthropic" && prev.markers && prev.cached > 0 {
		base = prev.cached // only what was actually written can be read
	}
	ca.ExpectedRead = min(ca.SharedTokens, base)
	ca.MissedTokens = max(0, ca.ExpectedRead-read)
	if ca.MissedTokens <= missTolerance(p.Provider) || ca.MissedTokens*10 < ca.ExpectedRead {
		return ca
	}

	m := &Miss{Tokens: ca.MissedTokens, Detail: map[string]any{"prefix_segments": lcp}}
	ca.Miss = m
	switch {
	case prev.model != p.Model:
		m.Reason = ReasonModel
	case minCacheable(p.Provider, p.Model) > 0 && ca.SharedTokens < minCacheable(p.Provider, p.Model):
		m.Reason = ReasonBelowMin
		m.Detail["minimum_tokens"] = minCacheable(p.Provider, p.Model)
	case p.Provider == "anthropic" && !p.CacheMarkers:
		m.Reason = ReasonNoMarkers
	case a.gapExpired(p, prev, at):
		m.Reason = ReasonTTL
		m.Detail["gap_seconds"] = int64(at.Sub(prev.at).Seconds())
	case !equalHashes(toolSeq(p), prev.toolSeq):
		m.Reason = ReasonTools
		m.Detail["change"] = toolChange(toolSeq(p), prev.toolSeq)
	default:
		if cat, d, lines, ok := volatileSpan(prev, p, lcp); ok {
			m.Reason = ReasonVolatile
			m.Detail["content"] = cat
			m.Detail["segment_index"] = d
			m.Detail["token_offset"] = off[d]
			m.Detail["changed_lines"] = lines
		} else {
			m.Reason = ReasonUnknown
		}
	}
	return ca
}

func (a *Analyzer) gapExpired(p *Parsed, prev *snapshot, at time.Time) bool {
	ttl := cacheTTLSeconds(p.Provider, p.OneHourTTL || prev.oneHour)
	return ttl > 0 && at.Sub(prev.at) > time.Duration(ttl)*time.Second
}

func toolSeq(p *Parsed) []hash {
	var out []hash
	for _, s := range p.Segments {
		if s.Kind == KindToolDef {
			out = append(out, s.Hash)
		}
	}
	return out
}

func equalHashes(a, b []hash) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func toolChange(cur, prev []hash) string {
	set := func(h []hash) map[hash]bool {
		m := map[hash]bool{}
		for _, x := range h {
			m[x] = true
		}
		return m
	}
	c, p := set(cur), set(prev)
	added, removed := 0, 0
	for h := range c {
		if !p[h] {
			added++
		}
	}
	for h := range p {
		if !c[h] {
			removed++
		}
	}
	switch {
	case added == 0 && removed == 0:
		return "reordered"
	case removed == 0:
		return "added"
	case added == 0:
		return "removed"
	}
	return "modified"
}

// volatileSpan reports a small change in a system-level segment at the first
// point the prompts diverge: same kind in the same slot, at most a few lines
// differ. It returns the category of the changed span (date, id, path, other),
// the segment index and the number of changed lines.
func volatileSpan(prev *snapshot, p *Parsed, d int) (string, int, int, bool) {
	if d >= len(p.Segments) || d >= len(prev.hashes) {
		return "", 0, 0, false
	}
	prevLines, ok := prev.lines[d]
	cur := p.Segments[d]
	if !ok || !wantsLines(cur) {
		return "", 0, 0, false
	}
	before := make(map[uint64]struct{}, len(prevLines))
	for _, l := range prevLines {
		before[l.h] = struct{}{}
	}
	changed, cat := 0, ""
	for _, l := range strings.Split(cur.text, "\n") {
		if _, same := before[hashLine(l)]; same {
			continue
		}
		changed++
		if changed > maxVolatileLines {
			return "", 0, 0, false
		}
		if c := spanCategory(l); cat == "" || cat == "other" {
			cat = c
		}
	}
	if changed == 0 { // lines moved or were removed rather than changed
		return "", 0, 0, false
	}
	return cat, d, changed, true
}

// sharedTokens estimates how many tokens of this request are content the
// earlier request also had, wherever it now sits: segments with an identical
// hash count in full, and for system-level segments in the same slot the lines
// that are unchanged count by byte share. This is what a stable prompt would
// have read from the cache. It is an estimate.
func sharedTokens(prev *snapshot, p *Parsed, e *Estimate) int64 {
	had := make(map[hash]struct{}, len(prev.hashes))
	for _, h := range prev.hashes {
		had[h] = struct{}{}
	}
	var shared int64
	for i, s := range p.Segments {
		if _, ok := had[s.Hash]; ok {
			shared += e.seg[i]
			continue
		}
		pl, ok := prev.lines[i]
		if !ok || i >= len(prev.hashes) || !wantsLines(s) || s.Bytes == 0 {
			continue
		}
		before := make(map[uint64]struct{}, len(pl))
		for _, l := range pl {
			before[l.h] = struct{}{}
		}
		same := 0
		for _, l := range lineHashes(s.text) {
			if _, ok := before[l.h]; ok {
				same += int(l.n)
			}
		}
		shared += e.seg[i] * int64(min(same, s.Bytes)) / int64(s.Bytes)
	}
	return shared
}
