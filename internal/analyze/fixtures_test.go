package analyze

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/pricing"
)

// Synthetic sessions. Nothing here is real traffic; sizes are chosen so that
// byte counts, token estimates and dollars can be worked out by hand.

var t0 = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

const testModel = "claude-opus-4-8" // 1,024-token minimum; $5/M input, $0.50/M cache read, $6.25/M 5m write

// pad returns about n bytes of deterministic filler that contains no dates,
// paths or ids, so it never looks like volatile content.
func pad(seed string, n int) string {
	var sb strings.Builder
	for i := 0; sb.Len() < n; i++ {
		fmt.Fprintf(&sb, "%s line %d lorem ipsum dolor sit amet\n", seed, i)
	}
	return sb.String()[:n]
}

type block map[string]any

func text(s string) block { return block{"type": "text", "text": s} }
func toolUse(id, name string) block {
	return block{"type": "tool_use", "id": id, "name": name, "input": id}
}
func toolResult(id, content string, isErr bool) block {
	b := block{"type": "tool_result", "tool_use_id": id, "content": content}
	if isErr {
		b["is_error"] = true
	}
	return b
}

type message struct {
	role   string
	blocks []block
}

func user(bs ...block) message      { return message{"user", bs} }
func assistant(bs ...block) message { return message{"assistant", bs} }

type req struct {
	model   string
	system  []string
	tools   []string
	msgs    []message
	caching bool // put cache_control on the last system block
}

func (r req) body() []byte {
	if r.model == "" {
		r.model = testModel
	}
	var sys []block
	for i, s := range r.system {
		b := text(s)
		if r.caching && i == len(r.system)-1 {
			b["cache_control"] = map[string]string{"type": "ephemeral"}
		}
		sys = append(sys, b)
	}
	var tools []block
	for _, n := range r.tools {
		tools = append(tools, block{"name": n, "description": "tool " + n, "input_schema": map[string]any{"type": "object"}})
	}
	var msgs []block
	for _, m := range r.msgs {
		msgs = append(msgs, block{"role": m.role, "content": m.blocks})
	}
	top := block{"model": r.model, "max_tokens": 1024, "messages": msgs}
	if len(sys) > 0 {
		top["system"] = sys
	}
	if len(tools) > 0 {
		top["tools"] = tools
	}
	b, err := json.Marshal(top)
	if err != nil {
		panic(err)
	}
	return b
}

// bodyTokens is the prompt size a provider would report if it counted 4
// bytes per token over the segments.
func bodyTokens(t *testing.T, body []byte) int64 {
	t.Helper()
	p, ok := Parse("anthropic", "/v1/messages", body)
	if !ok {
		t.Fatal("fixture did not parse")
	}
	var n int
	for _, s := range p.Segments {
		n += s.Bytes
	}
	return int64(n / 4)
}

// turn is one request/response of a session, with the usage the "provider" reported.
type turn struct {
	at     time.Duration // since t0
	body   []byte
	read   int64 // cache-read tokens; 0 with write 0 means nothing was cached
	write  int64 // 5m cache-write tokens
	output int64
}

func (tn turn) input(t *testing.T, key string) Input {
	t.Helper()
	total := bodyTokens(t, tn.body)
	in := Input{
		Provider: "anthropic", Path: "/v1/messages", Body: tn.body, SessionKey: key, Start: t0.Add(tn.at), Reported: true,
		CacheWriteTotal: tn.write,
		Usage:           pricing.Usage{UncachedInput: total - tn.read - tn.write, CacheRead: tn.read, Output: tn.output},
	}
	if tn.write > 0 {
		in.Usage.CacheWrite = map[string]int64{"5m": tn.write}
	}
	if in.Usage.UncachedInput < 0 {
		t.Fatalf("fixture reads %d + writes %d more than the %d-token prompt", tn.read, tn.write, total)
	}
	return in
}

// run feeds turns to a fresh analyzer and returns the findings of each.
func run(t *testing.T, a *Analyzer, key string, turns ...turn) [][]Finding {
	t.Helper()
	var all [][]Finding
	for _, tn := range turns {
		all = append(all, analyzeOne(t, a, tn.input(t, key)))
	}
	return all
}

func analyzeOne(t *testing.T, a *Analyzer, in Input) []Finding {
	t.Helper()
	out := a.Analyze(in)
	if out == nil {
		t.Fatal("request was not analyzed")
	}
	return decodeFindings(t, out)
}

func decodeFindings(t *testing.T, out *Output) []Finding {
	t.Helper()
	if len(out.Findings) == 0 {
		return nil
	}
	var fs []Finding
	if err := json.Unmarshal(out.Findings, &fs); err != nil {
		t.Fatal(err)
	}
	return fs
}

func find(fs []Finding, det string) *Finding {
	for i := range fs {
		if fs[i].Detector == det {
			return &fs[i]
		}
	}
	return nil
}

func missReason(fs []Finding) string {
	f := find(fs, DetCacheMiss)
	if f == nil {
		return ""
	}
	r, _ := f.Detail["reason"].(string)
	return r
}

func newTestAnalyzer(t *testing.T, mod func(*Options)) *Analyzer {
	t.Helper()
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	o := Options{Pricing: tbl}
	if mod != nil {
		mod(&o)
	}
	return New(o)
}

// cleanSession is a well-behaved cached conversation: appended turns, stable
// prefix, caching requested, every tool used, small tool results, large
// answers. No detector may fire on it.
func cleanSession() []turn {
	sys := []string{pad("system", 12000)}
	tools := []string{"Read", "Grep"}
	conv := []message{user(text("please look at the build"))}
	mk := func(msgs []message) []byte {
		return req{system: sys, tools: tools, msgs: msgs, caching: true}.body()
	}
	var turns []turn
	var prevTotal int64
	add := func(at time.Duration, body []byte, total int64) {
		tn := turn{at: at, body: body, output: 1500}
		if prevTotal > 0 {
			tn.read = prevTotal
			tn.write = total - prevTotal
		} else {
			tn.write = total
		}
		turns = append(turns, tn)
		prevTotal = total
	}
	bodyN := func() ([]byte, int64) {
		b := mk(conv)
		p, _ := Parse("anthropic", "/v1/messages", b)
		var n int
		for _, s := range p.Segments {
			n += s.Bytes
		}
		return b, int64(n / 4)
	}
	b, tot := bodyN()
	add(0, b, tot)
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("tu%d", i)
		name := tools[i%2]
		conv = append(conv, assistant(text("checking "+id), toolUse(id, name)), user(toolResult(id, pad("result"+id, 400)+fmt.Sprint(i), false)))
		b, tot = bodyN()
		add(time.Duration(i)*20*time.Second, b, tot)
	}
	return turns
}

func usageFor(uncached, output int64) pricing.Usage {
	return pricing.Usage{UncachedInput: uncached, Output: output}
}
