package hooks

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/hookcaps"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// The cold-resume guard (hook_cold_resume, technique B21). A prompt sent to a large session whose prompt cache has
// expired makes the next request write the whole context to the cache again. The guard says what that costs and,
// in "ask" mode, stops the first prompt after a cold resume once, so the user can send it again (and pay) or
// /clear and paste a short note instead.
//
// What it relies on was read on code.claude.com/docs/en/hooks on 2026-11-23 (docs/hooks.md, "Contract facts"):
//   - SessionStart with source "resume" or "fork" carries context_tokens, prompt_cache_likely_expired and
//     estimated_cache_write_usd. That is how a cold resume is known.
//   - UserPromptSubmit can block with decision "block"; the reason is shown to the user and not added to context,
//     and the block message ends with the original prompt, so nothing the user typed is lost.
//   - UserPromptSubmit also fires on turns Claude Code starts itself, and nothing in the input says which. So a
//     prompt after a mid-session idle gap is only ever warned about, never blocked.
//
// Nothing here writes text from the session anywhere: the note is built in memory and returned to Claude Code in
// the hook's reply (PRINCIPLES.md rule 7). Counts and token figures go to the spool. Any error means no reply,
// and the prompt goes through (rule 4).

// EventPromptSubmit runs on UserPromptSubmit.
const EventPromptSubmit = "user_prompt_cold_check"

// Session pins of the guard. Values are digits, fixed words and a model id checked against a strict pattern, so
// they never hold a space (the ledger's separator).
const (
	// pinColdSettings is "<action>|<min tokens>|<min week pct>", pinned at SessionStart (rule 5).
	pinColdSettings = "c:cold_settings"
	// pinColdArm is "<context tokens>|<estimated usd or ->|<model or ->" after a cold resume, until the next prompt.
	pinColdArm = "c:cold_arm"
	// pinColdPending is "<tokens>" after the guard blocked a prompt, until the session's next prompt or its end.
	pinColdPending = "c:cold_pending"
	pinCleared     = "-"
)

const (
	// coldNoteMaxBytes caps the note. At 3.5 bytes per token it costs at most about 430 tokens if pasted; the
	// registry declares 450 (TestColdNoteFitsItsDeclaredOverhead).
	coldNoteMaxBytes   = 1500
	coldNotePrompts    = 3
	coldNotePromptMax  = 300
	coldNoteFiles      = 10
	coldNoteReplyMax   = 500
	coldTranscriptTail = 1 << 20
	// coldMarkerMaxAge is how long the "fired" marker of a session is kept. A session resumed after that may be
	// stopped once more.
	coldMarkerMaxAge = 30 * 24 * time.Hour
	// coldDefaultTTL is used when neither the status line nor the transcript says which cache lifetime is in
	// effect. It is the longer one, so the guard calls a cache cold late rather than early (rule 9).
	coldDefaultTTL = time.Hour
)

// coldResumeTestHook runs before the guard decides; tests use it to make the hook panic or stall.
var coldResumeTestHook func()

// coldSettings is a session's pinned cold-resume settings.
type coldSettings struct {
	action     string
	minTokens  int64
	minWeekPct float64
}

func (s ColdResumeSettings) pin() string {
	c := s.settings()
	return c.action + "|" + strconv.FormatInt(c.minTokens, 10) + "|" + strconv.FormatFloat(c.minWeekPct, 'f', -1, 64)
}

// settings fills in the defaults for a snapshot written before the guard existed.
func (s ColdResumeSettings) settings() coldSettings {
	c := coldSettings{action: s.Action, minTokens: s.MinTokens, minWeekPct: s.MinWeekPct}
	if c.action != config.ColdResumeAsk {
		c.action = config.ColdResumeWarn
	}
	if c.minTokens <= 0 {
		c.minTokens = 150000
	}
	if c.minWeekPct <= 0 {
		c.minWeekPct = 3
	}
	return c
}

func parseColdSettings(v string) (coldSettings, bool) {
	f := strings.Split(v, "|")
	if len(f) != 3 || (f[0] != config.ColdResumeWarn && f[0] != config.ColdResumeAsk) {
		return coldSettings{}, false
	}
	n, e1 := strconv.ParseInt(f[1], 10, 64)
	p, e2 := strconv.ParseFloat(f[2], 64)
	if e1 != nil || e2 != nil || n <= 0 || !(p > 0) {
		return coldSettings{}, false
	}
	return coldSettings{action: f[0], minTokens: n, minWeekPct: p}, true
}

// armColdResume is called by session_reset at SessionStart. It pins the settings and, after a resume or fork that
// Claude Code says found the cache expired, arms the guard for the session's next prompt.
func (h *handler) armColdResume(in Input, st *sessionState) {
	if _, ok := st.Pins[pinColdSettings]; !ok {
		st.do(op{T: "pin", N: pinColdSettings, V: h.snap.ColdResume.pin()})
	}
	if in.Event != "SessionStart" || (in.Source != "resume" && in.Source != "fork") {
		return
	}
	if in.PromptCacheLikelyExpired == nil || !*in.PromptCacheLikelyExpired || in.ContextTokens == nil || *in.ContextTokens <= 0 {
		return
	}
	usd, model := "-", "-"
	if v := in.EstimatedCacheWriteUSD; v != nil && *v > 0 && *v < 1e6 {
		usd = strconv.FormatFloat(*v, 'f', 6, 64)
	}
	if store.ValidModelID(in.Model) {
		model = in.Model
	}
	st.do(op{T: "pin", N: pinColdArm, V: strconv.FormatInt(*in.ContextTokens, 10) + "|" + usd + "|" + model})
}

// coldCase says which of the two situations a prompt is in.
type coldCase int

const (
	caseNone   coldCase = iota
	caseResume          // the first prompt after a resume Claude Code reported cold
	caseIdle            // a prompt after an idle gap longer than the cache lifetime, in the same process
)

// rebuild is what continuing costs: the tokens the next request writes to the cache again, with estimates.
type rebuild struct {
	tokens int64
	usd    float64 // API list prices; 0 = unknown
	usdBy  string  // who estimated it: "Claude Code" or "Trove"
	model  string
	ttl    time.Duration
	limits *limitfit.Estimate // share of the plan limits; nil without a fit
}

// coldDecision is what the guard does with one prompt.
type coldDecision struct {
	kind  string // spool kind, "" for nothing
	block bool
	warn  bool
}

// decideCold is the pure decision: given the pinned state and settings, the case, the rebuild and whether a block
// can reach a typed prompt on this run, what to do. The case and the size gate come first; only then the mode.
func decideCold(state string, s coldSettings, c coldCase, r rebuild, canBlock bool) coldDecision {
	if state == stateOff || c == caseNone || !bigRebuild(s, r) {
		return coldDecision{}
	}
	if state == stateShadow {
		if c == caseResume {
			return coldDecision{kind: KindWouldBlock} // what "ask" would do
		}
		return coldDecision{kind: KindWouldAdvise}
	}
	if s.action == config.ColdResumeAsk && c == caseResume && canBlock {
		return coldDecision{kind: KindBlocked, block: true}
	}
	return coldDecision{kind: KindAdvised, warn: true}
}

// bigRebuild applies the size gate: enough tokens, or the low end of the weekly share's interval at the threshold.
func bigRebuild(s coldSettings, r rebuild) bool {
	if r.tokens >= s.minTokens {
		return true
	}
	return r.limits != nil && r.limits.Week != nil && r.limits.Week.Lo >= s.minWeekPct
}

// promptSubmit is the UserPromptSubmit hook.
func (h *handler) promptSubmit(in Input, key string) ([]byte, error) {
	if h.agent != AgentClaude {
		return nil, nil
	}
	if coldResumeTestHook != nil {
		coldResumeTestHook()
	}
	st := loadSession(h.dir, key)
	defer func() { _ = st.save(h.dir, key) }()

	// A prompt after a block is the user's answer: they sent it again. Record it once and let it through.
	if v := st.Pins[pinColdPending]; v != "" && v != pinCleared {
		n, _ := strconv.ParseInt(v, 10, 64)
		st.do(op{T: "pin", N: pinColdPending, V: pinCleared})
		return nil, h.emit(key, config.OptimizerHookColdResume, KindOverridden, 0, n)
	}
	arm := st.Pins[pinColdArm]
	if arm != "" && arm != pinCleared {
		st.do(op{T: "pin", N: pinColdArm, V: pinCleared}) // only the first prompt after the resume
	}
	state := h.snap.State(config.OptimizerHookColdResume, st.Pins[config.OptimizerHookColdResume], h.dir, h.getenv)
	if state == stateOff || coldFired(h.dir, key) {
		return nil, nil
	}
	s, ok := parseColdSettings(st.Pins[pinColdSettings])
	if !ok {
		return nil, nil // a session from before the guard existed
	}

	c, r := caseNone, rebuild{}
	if arm != "" && arm != pinCleared {
		c, r = caseResume, armedRebuild(arm, in.TranscriptPath)
	} else if t, ok := lastMainRequest(in.TranscriptPath); ok {
		r = rebuild{tokens: t.prompt, model: t.model, ttl: h.ttlInEffect(key, t)}
		if h.now().Sub(t.at) > r.ttl {
			c = caseIdle
		}
	}
	if c == caseNone {
		return nil, nil
	}
	h.priceRebuild(&r)
	d := decideCold(state, s, c, r, h.canBlockPrompt())
	if d.kind == "" {
		return nil, nil
	}
	if err := markColdFired(h.dir, key); err != nil {
		return nil, err // without the marker the guard could act twice; doing nothing is safe
	}
	if err := h.emit(key, config.OptimizerHookColdResume, d.kind, 0, r.tokens); err != nil {
		return nil, err
	}
	switch {
	case d.block:
		st.do(op{T: "pin", N: pinColdPending, V: strconv.FormatInt(r.tokens, 10)})
		return coldBlockJSON(coldBlockText(r, buildColdNote(in.TranscriptPath, in.Cwd))), nil
	case d.warn:
		return coldWarnJSON(coldWarnText(r, c, s.action)), nil
	}
	return nil, nil
}

// coldSessionEnd records that the user cleared the session after a block. It runs before the ledger is removed.
func (h *handler) coldSessionEnd(in Input, key string) {
	st := loadSession(h.dir, key)
	if v := st.Pins[pinColdPending]; v != "" && v != pinCleared && in.Reason == "clear" {
		n, _ := strconv.ParseInt(v, 10, 64)
		_ = h.emit(key, config.OptimizerHookColdResume, KindCleared, 0, n)
	}
	sweepColdMarkers(h.dir, h.now())
}

// canBlockPrompt says whether a block reaches a person on this run: the installed version was seen to block a
// typed prompt and to say when it runs unattended (hook-capabilities, `block`), and this run is not one of those.
func (h *handler) canBlockPrompt() bool {
	if !LoadCapabilities(h.dir).Supports(AgentClaude, hookcaps.ToolPrompt, hookcaps.PromptBlock) {
		return false
	}
	return interactiveRun(h.getenv)
}

// interactiveRun reads CLAUDE_CODE_ENTRYPOINT, which Claude Code is seen to set for its hooks ("cli" for an
// interactive session) but does not document. Anything else, including nothing, counts as unattended. It is only
// asked once a check on the installed version recorded that it tells `claude -p` apart (see canBlockPrompt).
func interactiveRun(getenv func(string) string) bool {
	return getenv != nil && getenv("CLAUDE_CODE_ENTRYPOINT") == "cli"
}

// armedRebuild reads the pin session_reset left: Claude Code's own figures. The transcript supplies only the cache
// lifetime of the last write, for the share of the limits.
func armedRebuild(v, transcript string) rebuild {
	f := strings.Split(v, "|")
	if len(f) != 3 {
		return rebuild{}
	}
	r := rebuild{ttl: coldDefaultTTL}
	r.tokens, _ = strconv.ParseInt(f[0], 10, 64)
	if usd, err := strconv.ParseFloat(f[1], 64); err == nil && usd > 0 {
		r.usd, r.usdBy = usd, "Claude Code"
	}
	if f[2] != "-" {
		r.model = f[2]
	}
	if t, ok := lastMainRequest(transcript); ok {
		if t.ttl > 0 {
			r.ttl = t.ttl
		}
		if r.model == "" {
			r.model = t.model
		}
	}
	return r
}

// ttlInEffect is the cache lifetime of the session's main conversation: the status line's report when the spool
// still holds one, else the lifetime of the newest cache write in the transcript, else the longer default.
func (h *handler) ttlInEffect(key string, t mainRequest) time.Duration {
	if s, ok := store.LatestSpooledLimitSample(h.dir, key); ok && s.CacheTTLSeconds != nil {
		return time.Duration(*s.CacheTTLSeconds) * time.Second
	}
	if t.ttl > 0 {
		return t.ttl
	}
	return coldDefaultTTL
}

// priceRebuild fills in the dollar figure (when Claude Code gave none) and the share of the plan limits. Writing N
// tokens to the cache at the lifetime in effect is the rebuild; both figures are estimates.
func (h *handler) priceRebuild(r *rebuild) {
	d := limitfit.Delta{Model: r.model}
	if r.ttl == time.Hour {
		d.Tokens[limitfit.TWrite1h] = r.tokens
	} else {
		d.Tokens[limitfit.TWrite5m] = r.tokens
	}
	if r.usd <= 0 && r.model != "" {
		if tbl, err := pricing.Default(); err == nil {
			limitfit.PriceDelta(tbl, &d, h.now())
			if d.USD > 0 {
				r.usd, r.usdBy = d.USD, "Trove"
			}
		}
	}
	d.USD = r.usd
	r.limits = limitfit.ReadCache(h.dir).Percent(d)
}

// mainRequest is the newest main-conversation request in a transcript.
type mainRequest struct {
	at     time.Time
	prompt int64 // input + cache read + cache write tokens
	model  string
	ttl    time.Duration // lifetime of the newest cache write seen; 0 = none seen
}

type transcriptUsage struct {
	Input         int64 `json:"input_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	Split         *struct {
		Eph5m int64 `json:"ephemeral_5m_input_tokens"`
		Eph1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// transcriptLine is the part of a Claude Code transcript line the guard reads. These fields are observed in
// transcripts, not documented (docs/logs.md); a line that does not fit is skipped.
type transcriptLine struct {
	Type             string `json:"type"`
	Timestamp        string `json:"timestamp"`
	IsSidechain      bool   `json:"isSidechain"`
	IsMeta           bool   `json:"isMeta"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	Message          *struct {
		Model   string           `json:"model"`
		Usage   *transcriptUsage `json:"usage"`
		Content json.RawMessage  `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// scanTranscriptTail calls fn for each main-conversation line in the last coldTranscriptTail bytes of a transcript.
func scanTranscriptTail(path string, fn func(transcriptLine)) bool {
	if path == "" {
		return false
	}
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err == nil && st.Size() > coldTranscriptTail {
		if _, err := f.Seek(st.Size()-coldTranscriptTail, io.SeekStart); err != nil {
			return false
		}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), coldTranscriptTail)
	for sc.Scan() {
		ln := sc.Bytes()
		if !bytes.Contains(ln, []byte(`"assistant"`)) && !bytes.Contains(ln, []byte(`"user"`)) {
			continue
		}
		var l transcriptLine
		if json.Unmarshal(ln, &l) != nil || l.IsSidechain || l.Message == nil {
			continue
		}
		fn(l)
	}
	return true
}

// lastMainRequest finds the newest main-conversation request with usage, and the lifetime of the newest cache
// write, reading the transcript's tail from the end so that only the last few lines are parsed: it runs on every
// prompt while the guard is on. ok is false when there is none.
func lastMainRequest(path string) (out mainRequest, ok bool) {
	b := readTail(path)
	for end := len(b); end > 0; {
		start := bytes.LastIndexByte(b[:end], '\n') + 1
		ln := b[start:end]
		end = start - 1
		if !bytes.Contains(ln, []byte(`"assistant"`)) || !bytes.Contains(ln, []byte(`"usage"`)) {
			continue
		}
		var l transcriptLine
		if json.Unmarshal(ln, &l) != nil || l.IsSidechain || l.Message == nil || l.Type != "assistant" ||
			l.Message.Usage == nil || !strings.HasPrefix(l.Message.Model, "claude-") {
			continue
		}
		if !ok {
			at, err := time.Parse(time.RFC3339Nano, l.Timestamp)
			if err != nil {
				continue
			}
			u := l.Message.Usage
			out, ok = mainRequest{at: at, model: l.Message.Model, prompt: u.Input + u.CacheRead + u.CacheCreation}, true
		}
		if s := l.Message.Usage.Split; s != nil {
			switch {
			case s.Eph1h > 0:
				out.ttl = time.Hour
			case s.Eph5m > 0:
				out.ttl = 5 * time.Minute
			}
		}
		if out.ttl > 0 {
			break
		}
	}
	return out, ok
}

// readTail reads the last coldTranscriptTail bytes of a file, dropping a first line the read cut. nil on any error.
func readTail(path string) []byte {
	if path == "" {
		return nil
	}
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	start := max(0, st.Size()-coldTranscriptTail)
	b := make([]byte, st.Size()-start)
	if _, err := f.ReadAt(b, start); err != nil && err != io.EOF {
		return nil
	}
	if start > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return b
}

// buildColdNote is the note the user can paste after /clear: their last few prompts, the files edited and the end
// of the last reply, from the transcript tail. It exists only in memory and in the hook's reply.
func buildColdNote(path, cwd string) string {
	var prompts []string
	var files []string
	reply := ""
	scanTranscriptTail(path, func(l transcriptLine) {
		switch l.Type {
		case "user":
			if p := typedPrompt(l); p != "" {
				prompts = append(prompts, p)
			}
		case "assistant":
			if !strings.HasPrefix(l.Message.Model, "claude-") {
				return // a line Claude Code wrote itself (model "<synthetic>")
			}
			var blocks []contentBlock
			if json.Unmarshal(l.Message.Content, &blocks) != nil {
				return
			}
			var text []string
			for _, b := range blocks {
				switch {
				case b.Type == "text" && strings.TrimSpace(b.Text) != "":
					text = append(text, strings.TrimSpace(b.Text))
				case b.Type == "tool_use":
					if f := editedFile(b, cwd); f != "" {
						files = append(remove(files, f), f)
					}
				}
			}
			if len(text) > 0 {
				reply = strings.Join(text, "\n")
			}
		}
	})
	if len(prompts) == 0 && len(files) == 0 && reply == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("Context from my previous session, which I cleared to avoid re-sending it:\n")
	if len(prompts) > 0 {
		b.WriteString("My last requests:\n")
		for _, p := range prompts[max(0, len(prompts)-coldNotePrompts):] {
			b.WriteString("- " + oneLine(p, coldNotePromptMax) + "\n")
		}
	}
	if len(files) > 0 {
		files = files[max(0, len(files)-coldNoteFiles):]
		b.WriteString("Files edited: " + strings.Join(files, ", ") + "\n")
	}
	if reply != "" {
		b.WriteString("End of your last reply: " + oneLine(tail(reply, coldNoteReplyMax), coldNoteReplyMax) + "\n")
	}
	return capBytes(b.String(), coldNoteMaxBytes)
}

// typedPrompt returns the text of a prompt the user typed, or "" for a tool result, a compaction summary, a line
// Claude Code wrote itself (isMeta) or a slash-command wrapper.
func typedPrompt(l transcriptLine) string {
	if l.IsMeta || l.IsCompactSummary || len(l.Message.Content) == 0 {
		return ""
	}
	var text string
	if l.Message.Content[0] == '"' {
		if json.Unmarshal(l.Message.Content, &text) != nil {
			return ""
		}
	} else {
		var blocks []contentBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return ""
		}
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				return ""
			case "text":
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, "\n")
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "<") {
		return ""
	}
	return text
}

// editedFile is the file an Edit, Write, MultiEdit or NotebookEdit call changed, relative to cwd when inside it.
func editedFile(b contentBlock, cwd string) string {
	switch b.Name {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
	default:
		return ""
	}
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	if json.Unmarshal(b.Input, &in) != nil {
		return ""
	}
	p := in.FilePath
	if p == "" {
		p = in.NotebookPath
	}
	if p == "" {
		return ""
	}
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(p)
}

func remove(xs []string, x string) []string {
	out := xs[:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

// oneLine collapses whitespace and cuts s to at most n bytes on a character boundary, marking a cut with "…".
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return capBytes(s, n-len("…")) + "…"
}

// tail keeps about the last n bytes of s, starting on a character boundary.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// capBytes cuts s to at most n bytes without splitting a character.
func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// The reply texts. Every figure says it is an estimate and who made it.

func coldCostText(r rebuild) string {
	var b strings.Builder
	fmt.Fprintf(&b, "about %s tokens", roundTokens(r.tokens))
	var parts []string
	if r.usd > 0 {
		parts = append(parts, fmt.Sprintf("about $%.2f at API prices (estimated by %s)", r.usd, r.usdBy))
	}
	if e := r.limits; e != nil {
		if s := e.FiveHour; s != nil {
			parts = append(parts, "~"+pctRange(s.Range)+" of your 5-hour limit")
		}
		if s := e.Week; s != nil {
			parts = append(parts, "~"+pctRange(s.Range)+" of your weekly limit")
		}
		basis := ""
		if e.FiveHour != nil {
			basis = limitfit.BasisText[e.FiveHour.Basis]
		} else if e.Week != nil {
			basis = limitfit.BasisText[e.Week.Basis]
		}
		if basis != "" {
			parts[len(parts)-1] += " (" + basis + ")"
		}
	}
	if len(parts) > 0 {
		b.WriteString(": " + strings.Join(parts, ", "))
	}
	return b.String()
}

func coldBlockText(r rebuild, note string) string {
	var b strings.Builder
	b.WriteString("Trove stopped this prompt once: this session's prompt cache has expired, so continuing writes its whole context to the cache again, " +
		coldCostText(r) + ".\n\n")
	b.WriteString("To continue anyway, send your prompt again (it is below). Trove will not stop this session again.\n")
	if note == "" {
		b.WriteString("To start fresh instead, type /clear. Clearing costs nothing.\n")
	} else {
		b.WriteString("To start fresh instead, type /clear, then paste this note with your prompt. Clearing costs nothing.\n\n")
		b.WriteString("--- note ---\n" + note + "--- end of note ---\n")
	}
	return b.String()
}

// coldWarnText is the warning. Its last sentence names the step that applies: with "warn" chosen, asking instead;
// with "ask" chosen after a resume, the check that lets Trove stop a prompt on this version; otherwise /clear alone.
func coldWarnText(r rebuild, c coldCase, action string) string {
	why := "this session's prompt cache had expired"
	if c == caseIdle {
		why = fmt.Sprintf("this session was idle longer than its %s prompt cache lifetime", ttlWords(r.ttl))
	}
	next := " To avoid it next time, /clear before a long break."
	switch {
	case action == config.ColdResumeWarn:
		next = " To avoid it next time, /clear before a long break, or set optimize.cold_resume_action = \"ask\"."
	case c == caseResume:
		next = " Trove did not stop the prompt because this Claude Code version has not passed `trove hooks verify --prompt-block` here."
	}
	return "Trove: " + why + ", so this prompt writes the whole context to the cache again, " + coldCostText(r) + "." + next
}

func coldBlockJSON(reason string) []byte {
	b, _ := json.Marshal(map[string]any{"decision": "block", "reason": reason})
	return b
}

func coldWarnJSON(msg string) []byte {
	b, _ := json.Marshal(map[string]any{"systemMessage": msg})
	return b
}

func roundTokens(n int64) string {
	if n >= 10000 {
		n = (n + 500) / 1000 * 1000
	}
	s := strconv.FormatInt(n, 10)
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func pctRange(r limitfit.Range) string {
	return pct1(r.Point) + "% (" + pct1(r.Lo) + "–" + pct1(r.Hi) + "%)"
}

func pct1(v float64) string {
	if v >= 10 || v == math.Trunc(v) {
		return strconv.FormatFloat(math.Round(v), 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

func ttlWords(d time.Duration) string {
	if d == time.Hour {
		return "1-hour"
	}
	return "5-minute"
}

// Markers: an empty file per session the guard acted on, so it acts at most once a session even across a resume
// (whether a resume keeps the session id is not documented). The name is the hashed session key.

func coldMarkerDir(dir string) string { return filepath.Join(hooksDir(dir), "coldguard") }

func coldMarkerPath(dir, key string) (string, bool) {
	if key == "" || len(key) > 80 || strings.ContainsAny(key, `/\.:`) {
		return "", false
	}
	return filepath.Join(coldMarkerDir(dir), key), true
}

func coldFired(dir, key string) bool {
	p, ok := coldMarkerPath(dir, key)
	if !ok {
		return true // a key that cannot be a file name: never act, rather than risk acting twice
	}
	_, err := os.Stat(p)
	return err == nil
}

func markColdFired(dir, key string) error {
	p, ok := coldMarkerPath(dir, key)
	if !ok {
		return fmt.Errorf("session key is not usable as a marker name")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // Trove's own data dir, hashed name
	if err != nil {
		return err
	}
	return f.Close()
}

func sweepColdMarkers(dir string, now time.Time) {
	entries, err := os.ReadDir(coldMarkerDir(dir))
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > coldMarkerMaxAge {
			_ = os.Remove(filepath.Join(coldMarkerDir(dir), e.Name()))
		}
	}
}
