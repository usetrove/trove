package journey

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/pricing"
)

// PricingDate is the fixed date every cost is priced at. Pricing is effective-dated,
// so a fixed date keeps results reproducible and makes the table date visible.
var PricingDate = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// simBudgetMS is the per-optimizer time budget used by the simulator. It is
// large on purpose: a short budget makes results depend on machine load.
const simBudgetMS = 60000

// bytesPerToken is the engine's own default conversion. It is an estimate; the
// live tier replaces it with provider-reported usage.
const bytesPerToken = optimize.DefaultBytesPerToken

// Tokens is a count of tokens by billing class.
type Tokens struct {
	UncachedInput int64 `json:"uncached_input"`
	CacheWrite    int64 `json:"cache_write"`
	CacheRead     int64 `json:"cache_read"`
	Output        int64 `json:"output"`
}

// Arm is what one session cost in one arm (Trove off or on).
type Arm struct {
	Requests int                `json:"requests"`
	Turns    int                `json:"turns"`
	Tokens   Tokens             `json:"tokens"`
	CostUSD  map[string]float64 `json:"cost_usd"`  // by pinned model ID
	FailOpen int                `json:"fail_open"` // requests the engine forwarded unchanged after a failure
	// FailOpenBy counts, per optimizer, the requests it failed in (timeout or error).
	FailOpenBy map[string]int   `json:"fail_open_by_optimizer,omitempty"`
	Applied    map[string]int64 `json:"tokens_removed_by_optimizer,omitempty"`
	Checks     Checks           `json:"checks"`
	// Hook-level only (subscription model). ContextTokens is the sum over requests
	// of the context the agent processed, in estimated tokens. Denied counts reads
	// the hooks blocked; Rewritten counts tool inputs the hooks would rewrite.
	ContextTokens int64 `json:"context_tokens,omitempty"`
	Denied        int   `json:"denied_reads,omitempty"`
	Rewritten     int   `json:"rewritten_inputs,omitempty"`
}

// Checks are objective, non-model checks on what was sent upstream. They do not
// say whether the agent would have finished the task; only the live tier can.
type Checks struct {
	// Structure: every tool call still has its result, in the same ids.
	Structure bool `json:"structure"`
	// VerbatimRecovered: every blob the recovery store kept is byte-identical to
	// one of this session's tool results. False means the store holds a version
	// that differs from what the tool printed (for example after noise removal).
	// That is reported as a finding; it is not counted as a structural failure.
	VerbatimRecovered bool `json:"verbatim_recovered"`
	// Stored is how many full-text blobs the recovery store kept.
	Stored int `json:"stored_blobs"`
}

// simModels are the pinned models every session is priced on. The first is the
// live pilot's model; the rest are estimates only.
var simModels = []string{"claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5"}

// sessionDriver produces the bodies for one session and runs them through an arm.
type sessionDriver struct {
	profile Profile
	table   *pricing.Table
	system  string
	toolPad string // padding for the agent's own tool definitions
	trove   []byte // extra tool definition added in the on arm (MCP schema size)
	cfg     config.Config
}

// newDriver prepares the constant parts of a profile's requests.
// DefaultSystemTokens is the base prompt a real Claude Code session sends before its
// first message: system prompt, tool definitions and project context. Measured in the
// developer's session logs as the median first-request cache write (about 19,000 tokens).
const DefaultSystemTokens = 19000

func newDriver(p Profile, table *pricing.Table, mcpBytes int) *sessionDriver {
	return newDriverWith(p, table, mcpBytes, DefaultSystemTokens)
}

// newDriverWith builds a driver whose base prompt is systemTokens (system prompt plus
// the agent's own tool definitions, which are part of the same prefix).
func newDriverWith(p Profile, table *pricing.Table, mcpBytes, systemTokens int) *sessionDriver {
	d := &sessionDriver{profile: p, table: table}
	sysBytes := int(float64(systemTokens)*bytesPerToken) - p.ToolsDescBytes
	if sysBytes < 1000 {
		sysBytes = 1000
	}
	d.system = fixedSystem(sysBytes)
	d.toolPad = pad(p.ToolsDescBytes)
	if mcpBytes > 0 {
		d.trove = []byte(fmt.Sprintf(`{"name":"trove_tools","description":%s,"input_schema":{"type":"object"}}`, jstr(pad(mcpBytes))))
	}
	d.cfg = config.Default()
	// The engine gives each optimizer a wall-clock budget (25 ms by default). On a
	// loaded machine that can cut an optimizer short and change its output, which
	// would make the simulation depend on the machine. The simulator gives the
	// budget room so the result depends only on the seed.
	d.cfg.Optimize.BudgetMS = simBudgetMS
	for _, name := range optimize.PipelineNames {
		d.cfg.Optimizers[name] = config.StateOn
	}
	return d
}

// header renders the constant part of a request: the model, the system prompt
// and the tool definitions. The on arm also carries Trove's tool definitions.
func (d *sessionDriver) headerFor(model string, withTrove bool) []byte {
	var b bytes.Buffer
	sys, _ := json.Marshal(d.system)
	fmt.Fprintf(&b, `{"model":%s,"max_tokens":4096,"system":%s,"tools":[`, jstr(model), sys)
	fmt.Fprintf(&b, `{"name":"agent_tools","description":%s,"input_schema":{"type":"object"}}`, jstr(d.toolPad))
	if withTrove && d.trove != nil {
		b.WriteByte(',')
		b.Write(d.trove)
	}
	b.WriteString(`],"messages":[`)
	return b.Bytes()
}

// bodies returns one request body per turn of the session, built from the
// original history. Request k holds the prompt and turns 0..k-1; the agent
// always sends its full history, and Trove transforms each request from that.
func (d *sessionDriver) bodies(turns []Turn, model string, withTrove bool) [][]byte {
	head := d.headerFor(model, withTrove)
	prompt := fmt.Sprintf(`{"role":"user","content":[{"type":"text","text":%s}]}`, jstr(userPrompt(d.profile.ID)))
	frags := make([]string, 0, 2*len(turns)+1)
	frags = append(frags, prompt)
	out := make([][]byte, 0, len(turns))
	for _, t := range turns {
		var body bytes.Buffer
		body.Write(head)
		body.WriteString(strings.Join(frags, ","))
		body.WriteString(`]}`)
		out = append(out, body.Bytes())
		frags = append(frags,
			fmt.Sprintf(`{"role":"assistant","content":[{"type":"text","text":%s},{"type":"tool_use","id":%s,"name":%s,"input":%s}]}`,
				jstr(reply(t.OutputTokens)), jstr(t.ToolUseID), jstr(t.ToolName), t.ToolInput),
			fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":%s,"content":%s}]}`,
				jstr(t.ToolUseID), jstr(t.Output)),
		)
	}
	return out
}

// runArm runs the requests of one session through one arm and prices them.
// Off sends the bodies unchanged; on runs the real engine on each request.
//
// Token counts use the pilot model's request bodies. The other models are priced
// on the same tokens, so their numbers are estimates of the same workload.
func (d *sessionDriver) runArm(turns []Turn, sessionIndex int, withTrove bool) (Arm, error) {
	arm := Arm{Turns: len(turns), CostUSD: map[string]float64{}, Applied: map[string]int64{}}
	arm.Checks = Checks{Structure: true}
	store := newMemStore()
	var eng *optimize.Engine
	if withTrove {
		eng = optimize.New(optimize.Options{
			Config:   d.cfg,
			Getenv:   func(string) string { return "" },
			Now:      func() time.Time { return PricingDate },
			Recovery: store,
		})
	}
	ids := make([]string, 0, len(turns))
	for _, t := range turns {
		ids = append(ids, t.ToolUseID)
	}

	var prev []byte
	for k, body := range d.bodies(turns, simModels[0], withTrove) {
		if eng != nil {
			res := eng.TransformRequest(context.Background(), optimize.RequestIn{
				Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: body,
			}, optimize.Session{Key: fmt.Sprintf("s%d-r%d", sessionIndex, k), Agent: "claude_code", TroveRun: true})
			if len(res.Failures) > 0 {
				arm.FailOpen++
				if arm.FailOpenBy == nil {
					arm.FailOpenBy = map[string]int{}
				}
				for _, fl := range res.Failures {
					arm.FailOpenBy[fl.Optimizer]++
				}
			}
			for _, rep := range res.Applied {
				arm.Applied[rep.Name] += int64(rep.TokensRemoved)
			}
			body = res.Body
		}
		if err := checkStructure(body, ids[:k]); err != nil {
			arm.Checks.Structure = false
		}
		// Cache model: the cached prefix is the part of this request that matches
		// the previous request byte for byte; the rest is written to cache. This
		// charges Trove whenever it rewrites the prefix. Uncached input is zero
		// because the whole prompt is cached after the first request.
		prefix := commonPrefix(prev, body)
		read := tokens(prefix)
		write := tokens(len(body)) - read
		out := int64(turns[k].OutputTokens)
		arm.Requests++
		arm.Tokens.CacheRead += read
		arm.Tokens.CacheWrite += write
		arm.Tokens.Output += out
		u := pricing.Usage{CacheRead: read, CacheWrite: map[string]int64{"5m": write}, Output: out}
		for _, m := range simModels {
			c := d.table.Cost(pricing.Request{Provider: "anthropic", Model: m, Time: PricingDate, Usage: u})
			if c.Status != pricing.Priced {
				return Arm{}, fmt.Errorf("model %s is not priced on %s: %s", m, PricingDate.Format("2006-01-02"), c.Reason)
			}
			arm.CostUSD[m] += c.Total.Float64()
		}
		prev = body
	}
	arm.Checks.Stored = store.count()
	arm.Checks.VerbatimRecovered = store.allOriginal(originalHashes(turns))
	return arm, nil
}

func tokens(bytesLen int) int64 { return int64(float64(bytesLen)/bytesPerToken + 0.999999) }

func commonPrefix(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

func originalHashes(turns []Turn) map[string]bool {
	m := map[string]bool{}
	for _, t := range turns {
		m[sumOf(t.Output)] = true
	}
	return m
}

func sumOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}

// checkStructure parses a request and confirms every expected tool call still
// has its result with the same id.
func checkStructure(body []byte, ids []string) error {
	var v struct {
		Messages []struct {
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, m := range v.Messages {
		for _, c := range m.Content {
			if c.Type == "tool_result" {
				seen[c.ToolUseID] = true
			}
		}
	}
	for _, id := range ids {
		if !seen[id] {
			return fmt.Errorf("tool result %s missing", id)
		}
	}
	return nil
}

// memStore is an in-memory recovery store. It keeps the full text the trimmer shortened.
type memStore struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func newMemStore() *memStore { return &memStore{blobs: map[string][]byte{}} }

// Put implements optimize.RecoveryStore.
func (m *memStore) Put(data []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := sumOf(string(data))
	m.blobs[id] = append([]byte(nil), data...)
	return id, nil
}

func (m *memStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.blobs)
}

// allOriginal reports whether every stored blob is one of the session's originals.
func (m *memStore) allOriginal(originals map[string]bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.blobs {
		if !originals[sumOf(string(b))] {
			return false
		}
	}
	return true
}

// jstr encodes s as a JSON string. Go's %q is not JSON: it emits escapes such as
// \x1b that a JSON parser rejects, which would make every request malformed.
func jstr(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err) // a string always marshals
	}
	return string(b)
}

// pad returns n bytes of plain text. It is filler for size, never shown to anyone.
func pad(n int) string {
	const unit = "Use the tool that matches the task and report results plainly. "
	if n <= 0 {
		return ""
	}
	return strings.Repeat(unit, n/len(unit)+1)[:n]
}

func fixedSystem(n int) string {
	return "You are a coding agent working in the user's repository. " + pad(n)
}

func userPrompt(profile string) string {
	return fmt.Sprintf("Continue the task from yesterday in the %s workspace and keep the tests green.", profile)
}

func reply(tokens int) string {
	const s = "I will check the next file and compare it with the test expectation. "
	n := int(float64(tokens) * bytesPerToken)
	return strings.Repeat(s, n/len(s)+1)[:n]
}
