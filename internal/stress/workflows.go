package stress

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/pricing"
)

// Scripted workflows: hand-written sessions shaped like everyday Claude Code work (fix a failing test, rename
// across a codebase, debug with logs, upgrade a dependency, explore and review, a long feature session, and a
// quick question). Every byte of tool output is generated text: no real code, paths or secrets.
//
// What they can show: what each optimizer does on these patterns, that cached prefixes stay byte-identical, that the
// engine is deterministic, and how long the engine takes. What they cannot show: how much a real user saves. The
// shape of each session (how often a file is re-read, how noisy a shell is) is an assumption written down in this
// file, not a measurement. Run `trove bench stress --replay-dir` on real logs for that.

// WorkflowRow is one scripted workflow, run with the engine off and on.
type WorkflowRow struct {
	Name          string  `json:"name"`
	Tier          string  `json:"tier"` // "everyday" or "heavy"
	Description   string  `json:"description"`
	Turns         int     `json:"turns"`
	Requests      int     `json:"requests"`
	InputTokens   int64   `json:"input_tokens_off"` // estimated: bytes over the engine's bytes-per-token ratio
	TokensCut     int64   `json:"tokens_removed"`   // estimated
	OverheadToks  int64   `json:"overhead_tokens"`  // declared by the optimizers that changed a request, charged in the on arm
	OffUSD        float64 `json:"cost_off_usd"`     // estimated at list price
	OnUSD         float64 `json:"cost_on_usd"`      // estimated at list price, including the declared overhead
	SavedPct      float64 `json:"saved_pct"`        // estimated
	Violations    int     `json:"violations"`       // broken invariants (measured by the checkers)
	PrefixOK      int     `json:"prefix_preserved"` // requests whose cached prefix stayed byte-identical (measured)
	Deterministic bool    `json:"deterministic"`    // a second run produced byte-identical requests (measured)
	KeyLines      int     `json:"key_lines"`        // failure, error and traceback lines in the original tool output (measured)
	KeyLinesKept  int     `json:"key_lines_kept"`   // of those, how many are still visible to the model after optimizing (measured)
	Trimmed       int     `json:"trimmed_outputs"`  // outputs the engine stored in the recovery store and replaced with a marker
}

// WorkflowReport is the whole run. Numbers are labelled: "estimated" ones depend on the scripted shape and list
// prices; "measured" ones are checks or timings taken during this run.
type WorkflowReport struct {
	Seed     uint64        `json:"seed"`
	Pricing  string        `json:"pricing_date"`
	Model    string        `json:"model"`
	Rows     []WorkflowRow `json:"workflows"`
	Headline Headline      `json:"headline"`
	Notes    []string      `json:"notes"`
}

// TierSummary is the estimated saving across one tier of workflows.
type TierSummary struct {
	Workflows int     `json:"workflows"`
	MedianPct float64 `json:"median_saved_pct"` // estimated
	MinPct    float64 `json:"min_saved_pct"`
	MaxPct    float64 `json:"max_saved_pct"`
}

// Headline is the figures worth quoting, each with its label and its limits.
type Headline struct {
	Everyday         TierSummary `json:"everyday"`
	Heavy            TierSummary `json:"heavy"`
	KeyLines         int         `json:"key_lines"`      // measured
	KeyLinesKept     int         `json:"key_lines_kept"` // measured
	Requests         int         `json:"requests"`
	Violations       int         `json:"violations"`        // measured
	PrefixPreserved  int         `json:"prefix_preserved"`  // measured
	AllDeterministic bool        `json:"all_deterministic"` // measured
	EngineP50ms      float64     `json:"engine_p50_ms"`     // measured, engine only
	EngineP99ms      float64     `json:"engine_p99_ms"`     // measured, engine only
}

// WorkflowOptions configures a run.
type WorkflowOptions struct {
	Seed  uint64
	Model string // default claude-sonnet-5-5
}

const workflowPrefixTokens = 18000 // Claude Code's system prompt, tool definitions and project context, roughly

type wfSpec struct {
	name, tier, desc string
	build            func(b *wfBuilder)
}

const (
	tierEveryday = "everyday"
	tierHeavy    = "heavy"
)

// RunWorkflows runs every scripted workflow twice (the second run checks determinism) and prices the first.
func RunWorkflows(o WorkflowOptions) (WorkflowReport, error) {
	if o.Model == "" {
		o.Model = "claude-sonnet-5-5"
	}
	tab, err := pricing.Default()
	if err != nil {
		return WorkflowReport{}, fmt.Errorf("the built-in pricing table did not load: %w", err)
	}
	rep := WorkflowReport{Seed: o.Seed, Pricing: PricingDate.Format("2006-01-02"), Model: o.Model}
	var lat []float64
	for _, w := range workflowSpecs() {
		row, l, err := runWorkflow(w, o, tab)
		if err != nil {
			return rep, err
		}
		lat = append(lat, l...)
		rep.Rows = append(rep.Rows, row)
	}
	byTier := map[string][]float64{}
	h := Headline{AllDeterministic: true}
	for _, r := range rep.Rows {
		byTier[r.Tier] = append(byTier[r.Tier], r.SavedPct)
		h.KeyLines += r.KeyLines
		h.KeyLinesKept += r.KeyLinesKept
		h.Requests += r.Requests
		h.Violations += r.Violations
		h.PrefixPreserved += r.PrefixOK
		h.AllDeterministic = h.AllDeterministic && r.Deterministic
	}
	summ := func(xs []float64) TierSummary {
		if len(xs) == 0 {
			return TierSummary{}
		}
		sort.Float64s(xs)
		return TierSummary{Workflows: len(xs), MedianPct: round1(quantile(xs, 0.5)), MinPct: round1(xs[0]), MaxPct: round1(xs[len(xs)-1])}
	}
	h.Everyday, h.Heavy = summ(byTier[tierEveryday]), summ(byTier[tierHeavy])
	sort.Float64s(lat)
	h.EngineP50ms, h.EngineP99ms = round2(quantile(lat, 0.5)), round2(quantile(lat, 0.99))
	rep.Headline = h
	rep.Notes = []string{
		"The workflows are scripted from typical Claude Code patterns. How often a file is re-read, how noisy a shell is and how long a session runs are assumptions written in internal/stress/workflows.go, not measurements of real users.",
		"Costs are estimates at list price on the pinned model and the pricing date shown. Token counts use the engine's bytes-per-token ratio, not provider counts. Declared optimizer overhead is charged to the optimized arm.",
		"The cache model is the realism layer's: the cached prefix is what matches the previous request byte for byte, and each turn has a 10% chance of a cold cache in both arms.",
		"Engine timings are measured in-process on this machine and exclude the gateway's own forwarding time.",
		"Savings from these sessions say what the optimizers do on these patterns. They are not a forecast of any user's bill.",
		"The heavy tier was written to be waste-rich. Its saving is where large, repetitive tool output dominates the session; do not quote it as what a typical session saves, and never average it with the everyday tier.",
		"A saving only counts if nothing was lost: every failure or error line in the original output must still be visible to the model, and every output the engine trimmed must be recoverable from the recovery store byte for byte. The report counts both.",
	}
	return rep, nil
}

func runWorkflow(w wfSpec, o WorkflowOptions, tab *pricing.Table) (WorkflowRow, []float64, error) {
	turns := buildWorkflow(w, o.Seed)
	spec := SessionSpec{Name: "wf-" + w.name, PrefixTokens: workflowPrefixTokens, Model: o.Model, Turns: len(turns)}
	bodies := buildBodies(spec, turns)

	first, lat, row := playWorkflow(spec, bodies, turns, o, tab)
	second, _, _ := playWorkflow(spec, bodies, turns, o, tab)
	row.Name, row.Tier, row.Description, row.Turns = w.name, w.tier, w.desc, len(turns)
	row.Deterministic = len(first) == len(second)
	for i := range first {
		if row.Deterministic && !bytes.Equal(first[i], second[i]) {
			row.Deterministic = false
		}
	}
	if math.IsNaN(row.OffUSD) || math.IsNaN(row.OnUSD) {
		return row, lat, fmt.Errorf("workflow %s: the pricing table could not price model %s at %s", w.name, o.Model, PricingDate.Format("2006-01-02"))
	}
	row.SavedPct = round1(savedPct(row.OffUSD, row.OnUSD))
	row.OffUSD, row.OnUSD = round4(row.OffUSD), round4(row.OnUSD)
	return row, lat, nil
}

// playWorkflow sends every request through a fresh engine and prices both arms. It returns the transformed
// bodies (for the determinism check) and the per-request engine time in milliseconds.
func playWorkflow(spec SessionSpec, bodies [][]byte, turns []turn, o WorkflowOptions, tab *pricing.Table) ([][]byte, []float64, WorkflowRow) {
	var row WorkflowRow
	store := &memStore{}
	eng := newEngine(allNames(), store, PricingDate)
	expiry := rng(o.Seed, "wf-expiry", spec.Name)
	var outs [][]byte
	var lat []float64
	var prevOff, prevOn []byte
	for k, orig := range bodies {
		expired := k > 0 && expiry.Float64() < 0.1
		t0 := time.Now()
		res := eng.TransformRequest(context.Background(), optimize.RequestIn{
			Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: orig,
		}, optimize.Session{Key: "wf-" + spec.Name, Agent: "claude_code", TroveRun: true})
		lat = append(lat, float64(time.Since(t0).Microseconds())/1000)
		out := res.Body
		outs = append(outs, out)

		overhead := declaredOverheadTokens(res)
		row.Requests++
		row.InputTokens += tokensOf(len(orig))
		if cut := tokensOf(len(orig)) - tokensOf(len(out)); cut > 0 {
			row.TokensCut += cut
		}
		row.OverheadToks += int64(overhead)
		vs := CheckRequest(orig, out, overhead)
		if len(res.Failures) > 0 && !bytes.Equal(out, orig) {
			vs = append(vs, Violation{InvFailOpenOnError, "a failed optimizer still changed the request"})
		}
		pv := CheckPrefix(prevOn, out)
		row.Violations += len(uniqueByInvariant(append(vs, pv...)))
		if len(pv) == 0 {
			row.PrefixOK++
		}
		row.OffUSD += requestUSD(tab, spec.Model, prevOff, orig, turns[k].Reply, expired)
		row.OnUSD += requestUSD(tab, spec.Model, prevOn, out, turns[k].Reply, expired) + overheadUSD(tab, spec.Model, int64(overhead))
		prevOff, prevOn = orig, out
	}
	if n := len(bodies); n > 0 {
		// The last request holds every tool output. Everything the engine stored must be byte-identical to an original,
		// and every failure line in the original must still be visible to the model (or have been a duplicate of one that is).
		row.Violations += len(CheckRecovery(bodies[n-1], store.snapshot()))
		row.Trimmed = len(store.snapshot())
		row.KeyLines, row.KeyLinesKept = keyLinesKept(bodies[n-1], outs[n-1])
		if row.KeyLinesKept < row.KeyLines {
			row.Violations++
		}
	}
	return outs, lat, row
}

// keyLinesKept counts the lines of the original request that report a failure (test failures, assertion errors, type
// errors, tracebacks, ERROR log lines) and how many of them are still present in the optimized request. A line the
// noise normalizer only cleaned (colors removed) still counts as present.
func keyLinesKept(orig, out []byte) (total, kept int) {
	// The request is JSON, so a tool output's line breaks are the two characters backslash and n, and a color code
	// is the six characters backslash-u-0-0-1-b. Work on that escaped text; a preserved line appears identically in out.
	seen := map[string]bool{}
	for _, raw := range strings.Split(string(orig), `\n`) {
		line := ansiRE.ReplaceAllString(raw, "")
		line = strings.TrimSpace(strings.TrimSuffix(line, `\r`))
		if len(line) < 12 || seen[line] || !isKeyLine(line) {
			continue
		}
		seen[line] = true
		total++
		if bytes.Contains(out, []byte(line)) {
			kept++
		}
	}
	return total, kept
}

var ansiRE = regexp.MustCompile(`\\u001b\[[0-9;]*[A-Za-z]`)

func isKeyLine(l string) bool {
	for _, m := range []string{"FAILED ", "AssertionError", "error TS", "Traceback", "ERROR ", "Error:", "FAIL "} {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// overheadUSD prices the tokens an optimizer declared it adds, as cache writes (the worst case: never read back).
func overheadUSD(tab *pricing.Table, model string, tokens int64) float64 {
	if tokens <= 0 {
		return 0
	}
	u := pricing.Usage{CacheWrite: map[string]int64{"5m": tokens}}
	c := tab.Cost(pricing.Request{Provider: "anthropic", Model: model, Time: PricingDate, Usage: u})
	if c.Status != pricing.Priced {
		return math.NaN()
	}
	return c.Total.Float64()
}

func round4(x float64) float64 { return math.Round(x*10000) / 10000 }

// --- the scripted sessions -------------------------------------------------------------------------------------

func workflowSpecs() []wfSpec {
	return []wfSpec{
		{"quick-question", tierEveryday, "Three turns: read a file, search, answer. The case where little can help.", wfQuick},
		{"fix-failing-test", tierEveryday, "pytest fails, read the test and source, edit, rerun twice until green.", wfFixTest},
		{"debug-with-logs", tierEveryday, "Run the app, read noisy colored logs, search them, fix, restart and read the same startup noise again.", wfDebugLogs},
		{"dependency-upgrade", tierEveryday, "npm install with progress noise, build warnings that repeat, CRLF test output, fix, rebuild.", wfUpgrade},
		{"rename-across-codebase", tierEveryday, "Search every usage, read and edit many files, type-check, re-read, fix, type-check again.", wfRename},
		{"explore-and-review", tierEveryday, "Orient in an unfamiliar repo: many reads, repeated status and diff, a second pass over key files.", wfExplore},
		{"long-feature-session", tierEveryday, "About 90 turns: explore, implement across files, test, debug, refactor. A normal long day.", wfLong},
		{"ci-failure-triage", tierHeavy, "Monorepo with a verbose test reporter (about 500 tests per run): four full runs while fixing failures one by one.", wfCITriage},
		{"container-build-debug", tierHeavy, "A failing container build: long build logs rerun after each Dockerfile fix, plus container logs.", wfContainerBuild},
		{"incident-log-forensics", tierHeavy, "Pull production logs repeatedly: long logs with the same stack trace over and over, and polling the same cluster status.", wfIncident},
	}
}

func buildWorkflow(w wfSpec, seed uint64) []turn {
	b := &wfBuilder{r: rng(seed, "wf", w.name), ver: map[string]int{}}
	w.build(b)
	return b.turns
}

type wfBuilder struct {
	r     *rand.Rand
	turns []turn
	ver   map[string]int // edits applied to a path so far; a re-read after an edit differs, a re-read without one is identical
}

func (b *wfBuilder) add(tool, input, out string) {
	i := len(b.turns)
	b.turns = append(b.turns, turn{ID: fmt.Sprintf("toolu_%04d", i), Name: tool, Input: input, Output: out, Reply: replyTokens(b.r)})
}

func (b *wfBuilder) read(path string, lines int) {
	b.add("Read", fmt.Sprintf(`{"file_path":%s}`, jstr(path)), fileBody(path, lines, b.ver[path]))
}

func (b *wfBuilder) edit(path string) {
	b.ver[path]++
	b.add("Edit", fmt.Sprintf(`{"file_path":%s,"old_string":%s,"new_string":%s}`, jstr(path), jstr("old_value = compute(x)"), jstr("old_value = compute(x, strict=True)")),
		fmt.Sprintf("The file %s has been updated. Here's the result of running `cat -n` on a snippet of the edited file:\n%s", path, snippet(path, b.ver[path])))
}

func (b *wfBuilder) grep(pat string, hits int) {
	var sb strings.Builder
	h := hash(pat)
	for i := 0; i < hits; i++ {
		fmt.Fprintf(&sb, "src/%s/%s.py:%d:    result = %s(value, %s)\n", pkgs[(h+uint64(i))%uint64(len(pkgs))], names[(h/3+uint64(i)*7)%uint64(len(names))], 10+i*13, pat, names[(h+uint64(i)*5)%uint64(len(names))])
	}
	b.add("Grep", fmt.Sprintf(`{"pattern":%s}`, jstr(pat)), sb.String())
}

func (b *wfBuilder) bash(cmd, out string) {
	b.add("Bash", fmt.Sprintf(`{"command":%s}`, jstr(cmd)), out)
}

var (
	pkgs  = []string{"api", "core", "billing", "auth", "jobs", "models", "utils", "web"}
	names = []string{"handler", "service", "client", "cache", "parser", "router", "store", "worker", "config", "schema"}
)

func hash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// fileBody is a Read result in cat -n format. The same (path, version) always gives the same text; a new version
// changes a few lines, as an edit would.
func fileBody(path string, lines, version int) string {
	h := hash(path)
	var sb strings.Builder
	for i := 1; i <= lines; i++ {
		var l string
		switch (h + uint64(i)) % 6 {
		case 0:
			l = fmt.Sprintf("def %s_%d(self, value, *, strict=False):", names[(h+uint64(i))%uint64(len(names))], i)
		case 1:
			l = fmt.Sprintf("    result = self.%s.fetch(value, timeout=%d)", names[(h/5+uint64(i))%uint64(len(names))], 5+i%30)
		case 2:
			l = "    if result is None:"
		case 3:
			l = fmt.Sprintf("        raise LookupError(f\"missing %s: {value!r}\")", names[(h/7+uint64(i))%uint64(len(names))])
		case 4:
			l = "    return normalize(result)"
		default:
			l = ""
		}
		if version > 0 && i%17 == version%17 {
			l += "  # strict" + fmt.Sprint(version)
		}
		fmt.Fprintf(&sb, "%6d\t%s\n", i, l)
	}
	return sb.String()
}

func snippet(path string, version int) string { return fileBody(path, 14, version) }

// pytestOut is pytest output. Timings differ run to run, so two runs are never byte-identical.
func pytestOut(r *rand.Rand, total int, failing []string, ansi bool) string {
	var sb strings.Builder
	sb.WriteString("============================= test session starts ==============================\n")
	sb.WriteString("platform linux -- Python 3.12.3, pytest-8.3.2, pluggy-1.5.0\nrootdir: /work/app\nplugins: cov-5.0.0, anyio-4.4.0\n")
	fmt.Fprintf(&sb, "collected %d items\n\n", total)
	for f := 0; f < 6; f++ {
		sb.WriteString(fmt.Sprintf("tests/test_%s.py ", names[f]) + strings.Repeat(".", 12+f) + fmt.Sprintf(" [%3d%%]\n", (f+1)*100/7))
	}
	if len(failing) > 0 {
		sb.WriteString("\n=================================== FAILURES ===================================\n")
		for _, t := range failing {
			fmt.Fprintf(&sb, "_______________________________ %s _______________________________\n\n", t)
			for i := 0; i < 8; i++ {
				fmt.Fprintf(&sb, "    def step_%d(self):\n>       assert self.%s.fetch(%d) == expected_%d\nE       AssertionError: assert None == %d\n", i, names[i], i, i, i)
			}
			sb.WriteString("\ntests/test_service.py:48: AssertionError\n")
		}
		sb.WriteString("=========================== short test summary info ============================\n")
		for _, t := range failing {
			fmt.Fprintf(&sb, "FAILED tests/test_service.py::%s - AssertionError\n", t)
		}
	}
	line := fmt.Sprintf("========================= %d failed, %d passed in %.2fs =========================\n", len(failing), total-len(failing), 8+r.Float64()*9)
	if len(failing) == 0 {
		line = fmt.Sprintf("============================== %d passed in %.2fs ==============================\n", total, 8+r.Float64()*9)
	}
	if ansi {
		line = "\x1b[32m" + line + "\x1b[0m"
	}
	return sb.String() + line
}

func logLines(r *rand.Rand, n int, startup bool) string {
	var sb strings.Builder
	if startup {
		sb.WriteString("\x1b[2m[boot]\x1b[0m loading config from ./config/app.yaml\n\x1b[2m[boot]\x1b[0m connecting to postgres at localhost:5432\n\x1b[32mINFO\x1b[0m  server listening on :8080\n")
	}
	for i := 0; i < n; i++ {
		lvl := "\x1b[32mINFO\x1b[0m "
		if r.IntN(7) == 0 {
			lvl = "\x1b[31mERROR\x1b[0m"
		}
		fmt.Fprintf(&sb, "2026-11-12T10:%02d:%02d.%03dZ %s request id=%08x path=/v1/%s status=%d took=%dms\n", i/60%60, i%60, r.IntN(1000), lvl, r.Uint32(), names[r.IntN(len(names))], []int{200, 200, 200, 404, 500}[r.IntN(5)], 3+r.IntN(300))
	}
	return sb.String()
}

func npmInstall(r *rand.Rand) string {
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&sb, "\r\x1b[2K(%s%s) ⠙ reify:%s-%d: timing reifyNode:node_modules/%s-%d", strings.Repeat("#", i/3), strings.Repeat(".", 14-i/3), names[i%len(names)], i, names[i%len(names)], i)
	}
	for i := 0; i < 9; i++ {
		fmt.Fprintf(&sb, "\r\nnpm \x1b[33mWARN\x1b[0m deprecated %s@%d.%d.0: this package is no longer maintained", names[i], 1+i%4, i)
	}
	fmt.Fprintf(&sb, "\r\n\r\nadded %d packages, and audited %d packages in %ds\r\n\r\n%d packages are looking for funding\r\n  run `npm fund` for details\r\n", 140+r.IntN(40), 400+r.IntN(80), 6+r.IntN(9), 31)
	return sb.String()
}

func buildWarnings() string {
	var sb strings.Builder
	sb.WriteString("> app@2.4.0 build\n> tsc -p . && vite build\n\n")
	for i := 0; i < 11; i++ {
		fmt.Fprintf(&sb, "\x1b[33m(!)\x1b[0m src/%s/%s.ts: Circular dependency: src/%s/%s.ts -> src/%s/index.ts -> src/%s/%s.ts\n", pkgs[i%8], names[i%10], pkgs[i%8], names[i%10], pkgs[i%8], pkgs[i%8], names[i%10])
	}
	sb.WriteString("vite v5.4.2 building for production...\n✓ 214 modules transformed.\ndist/assets/index-4f2a91c.js   412.18 kB │ gzip: 131.02 kB\n")
	return sb.String()
}

func tscErrors(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "src/%s/%s.ts(%d,%d): error TS2339: Property 'oldName%d' does not exist on type 'Service'.\n", pkgs[i%8], names[i%10], 12+i*3, 5+i%9, i%3)
	}
	if n > 0 {
		fmt.Fprintf(&sb, "\nFound %d errors in %d files.\n", n, min(n, 6))
	}
	return sb.String()
}

func gitStatus(modified int) string {
	var sb strings.Builder
	sb.WriteString("On branch feature/billing-retries\nYour branch is up to date with 'origin/feature/billing-retries'.\n\n")
	if modified > 0 {
		sb.WriteString("Changes not staged for commit:\n  (use \"git add <file>...\" to update what will be committed)\n")
		for i := 0; i < modified; i++ {
			fmt.Fprintf(&sb, "\tmodified:   src/%s/%s.py\n", pkgs[i%8], names[i%10])
		}
	} else {
		sb.WriteString("nothing to commit, working tree clean\n")
	}
	return sb.String()
}

func gitDiff(files int) string {
	var sb strings.Builder
	for i := 0; i < files; i++ {
		p := fmt.Sprintf("src/%s/%s.py", pkgs[i%8], names[i%10])
		fmt.Fprintf(&sb, "diff --git a/%s b/%s\nindex 3fa91c2..b7d04e1 100644\n--- a/%s\n+++ b/%s\n@@ -%d,7 +%d,8 @@ def %s_%d(self, value):\n", p, p, p, p, 20+i*9, 20+i*9, names[i%10], i)
		sb.WriteString("     result = self.client.fetch(value, timeout=10)\n-    return normalize(result)\n+    if result is None:\n+        raise LookupError(value)\n+    return normalize(result)\n")
	}
	return sb.String()
}

func wfQuick(b *wfBuilder) {
	b.read("src/billing/service.py", 120)
	b.grep("retry_policy", 6)
	b.read("src/billing/retry.py", 60)
}

func wfFixTest(b *wfBuilder) {
	b.bash("pytest -x -q", pytestOut(b.r, 148, []string{"test_refund_is_idempotent"}, false))
	b.read("tests/test_service.py", 210)
	b.read("src/billing/service.py", 260)
	b.grep("refund_id", 9)
	b.read("src/billing/refunds.py", 140)
	b.edit("src/billing/refunds.py")
	b.bash("pytest -x -q", pytestOut(b.r, 148, []string{"test_refund_is_idempotent"}, false))
	b.read("src/billing/refunds.py", 140) // after an edit: differs from the first read
	b.edit("src/billing/refunds.py")
	b.bash("pytest -q", pytestOut(b.r, 148, nil, false))
	b.bash("git status", gitStatus(2))
	b.bash("git diff", gitDiff(2))
}

func wfDebugLogs(b *wfBuilder) {
	b.bash("npm run dev &", logLines(b.r, 25, true))
	b.bash("curl -s localhost:8080/v1/orders/42", "{\"error\":\"internal\",\"request_id\":\"7f3a9c10\"}\n")
	b.bash("tail -n 200 logs/app.log", logLines(b.r, 200, false))
	b.grep("ERROR", 18)
	b.read("src/web/handler.py", 180)
	b.grep("request_id", 12)
	b.read("src/core/store.py", 220)
	b.edit("src/core/store.py")
	b.bash("npm run dev &", logLines(b.r, 25, true))
	b.bash("curl -s localhost:8080/v1/orders/42", "{\"id\":42,\"status\":\"paid\"}\n")
	b.bash("tail -n 200 logs/app.log", logLines(b.r, 200, false))
	b.read("src/core/store.py", 220)
}

func wfUpgrade(b *wfBuilder) {
	b.read("package.json", 48)
	b.bash("npm install vite@5 @vitejs/plugin-react@4", npmInstall(b.r))
	b.bash("npm ls --depth=0", strings.Repeat("├── pkg-a@1.2.3\n", 24))
	b.bash("npm run build", buildWarnings())
	b.bash("npm test", strings.ReplaceAll(pytestOut(b.r, 96, []string{"renders the dashboard"}, true), "\n", "\r\n"))
	b.read("vite.config.ts", 54)
	b.edit("vite.config.ts")
	b.bash("npm run build", buildWarnings())
	b.bash("npm test", strings.ReplaceAll(pytestOut(b.r, 96, nil, true), "\n", "\r\n"))
	b.bash("git diff", gitDiff(3))
}

func wfRename(b *wfBuilder) {
	b.grep("oldName", 22)
	files := []string{"src/api/handler.ts", "src/core/service.ts", "src/billing/client.ts", "src/auth/store.ts", "src/jobs/worker.ts", "src/web/router.ts", "src/models/schema.ts", "src/utils/parser.ts"}
	for _, f := range files {
		b.read(f, 130+int(hash(f)%90))
	}
	for _, f := range files {
		b.edit(f)
	}
	b.bash("npx tsc --noEmit", tscErrors(7))
	for _, f := range files[:3] {
		b.read(f, 130+int(hash(f)%90)) // re-read after the edit
	}
	b.edit(files[1])
	b.bash("npx tsc --noEmit", tscErrors(2))
	b.edit(files[2])
	b.bash("npx tsc --noEmit", "")
	b.bash("npx eslint src --max-warnings 0", strings.Repeat("\x1b[32m✓\x1b[0m checked\r\n", 3))
	b.bash("git status", gitStatus(8))
	b.bash("git status", gitStatus(8)) // checked twice, nothing changed
}

func wfExplore(b *wfBuilder) {
	b.bash("ls -la", strings.Repeat("drwxr-xr-x  8 dev dev  4096 Nov 12 09:14 src\n", 14))
	b.bash("git log --oneline -n 30", strings.Repeat("a1b2c3d fix: handle empty cart in checkout flow\n", 30))
	b.bash("git status", gitStatus(0))
	for _, f := range []string{"README.md", "package.json", "src/core/index.ts", "src/api/router.ts", "src/api/handler.ts", "src/core/service.ts", "src/billing/client.ts"} {
		b.read(f, 90+int(hash(f)%160))
	}
	b.grep("createOrder", 14)
	b.grep("createOrder", 14) // same search asked again, nothing changed
	b.read("src/api/handler.ts", 90+int(hash("src/api/handler.ts")%160))
	b.read("src/core/service.ts", 90+int(hash("src/core/service.ts")%160))
	b.bash("git diff main...HEAD --stat", strings.Repeat(" src/billing/client.ts | 42 +++++++++---\n", 12))
	b.bash("git diff main...HEAD", gitDiff(9))
	b.bash("git status", gitStatus(0))
	b.read("src/api/router.ts", 90+int(hash("src/api/router.ts")%160))
	b.grep("retryPolicy", 8)
	b.bash("git diff main...HEAD", gitDiff(9)) // second pass over the same diff
}

func wfLong(b *wfBuilder) {
	wfExplore(b)
	wfFixTest(b)
	wfRename(b)
	wfDebugLogs(b)
	wfUpgrade(b)
	for i := 0; i < 6; i++ {
		b.bash("pytest -q tests/test_service.py", pytestOut(b.r, 40, nil, false))
		b.bash("git status", gitStatus(3))
	}
}

// WorkflowMarkdown renders the report. Every figure carries its label.
func WorkflowMarkdown(r WorkflowReport) string {
	var sb strings.Builder
	h := r.Headline
	fmt.Fprintf(&sb, "# Scripted workflows: what the optimizers do on Claude Code sessions\n\n")
	fmt.Fprintf(&sb, "Model %s, list prices as of %s, seed %d. Scripted sessions, not real users: see the limits at the end.\n\n", r.Model, r.Pricing, r.Seed)
	sb.WriteString("## The numbers\n\n")
	fmt.Fprintf(&sb, "1. **Estimated saving, everyday sessions: median %.1f%% of list-price cost across %d workflows (range %.1f%% to %.1f%%).** Estimated.\n", h.Everyday.MedianPct, h.Everyday.Workflows, h.Everyday.MinPct, h.Everyday.MaxPct)
	fmt.Fprintf(&sb, "2. **Estimated saving, heavy-output sessions: median %.1f%% across %d workflows (range %.1f%% to %.1f%%).** Estimated. These sessions are dominated by large tool output (verbose test runs, build logs, incident logs) and were chosen because waste is high there. A ceiling for that kind of work, not a typical day.\n", h.Heavy.MedianPct, h.Heavy.Workflows, h.Heavy.MinPct, h.Heavy.MaxPct)
	fmt.Fprintf(&sb, "3. **Nothing lost, and cache safe: %d of %d failure and error lines in the original output are still visible to the model, trimmed output is recoverable byte for byte, %d of %d requests kept the cached prefix byte-identical, %d broken invariants, repeat runs byte-identical: %v.** Measured by the checkers in this run.\n", h.KeyLinesKept, h.KeyLines, h.PrefixPreserved, h.Requests, h.Violations, h.AllDeterministic)
	fmt.Fprintf(&sb, "4. **Engine time per request: p50 %.2f ms, p99 %.2f ms.** Measured in-process on this machine; excludes the gateway's forwarding time.\n\n", h.EngineP50ms, h.EngineP99ms)
	sb.WriteString("## By workflow\n\n| workflow | tier | turns | input tokens (est.) | tokens removed (est.) | outputs trimmed | cost off (est.) | cost on (est.) | saving (est.) | key lines kept | prefix kept | violations |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, w := range r.Rows {
		fmt.Fprintf(&sb, "| %s | %s | %d | %d | %d | %d | $%.4f | $%.4f | %.1f%% | %d/%d | %d/%d | %d |\n", w.Name, w.Tier, w.Turns, w.InputTokens, w.TokensCut, w.Trimmed, w.OffUSD, w.OnUSD, w.SavedPct, w.KeyLinesKept, w.KeyLines, w.PrefixOK, w.Requests, w.Violations)
	}
	sb.WriteString("\n")
	for _, w := range r.Rows {
		fmt.Fprintf(&sb, "- **%s** (%s): %s\n", w.Name, w.Tier, w.Description)
	}
	sb.WriteString("\n## Limits (say these when you quote the numbers)\n\n")
	for _, n := range r.Notes {
		fmt.Fprintf(&sb, "- %s\n", n)
	}
	return sb.String()
}

// --- heavy tier: sessions dominated by large tool output -------------------------------------------------------
// These are chosen to be waste-rich on purpose. They show what the optimizers do when output dominates the
// session (a monorepo CI loop, a container build, an incident), not what a typical session saves.

// verboseJest is a test reporter that lists every test (jest --verbose style), then the failures with stack frames.
func verboseJest(r *rand.Rand, tests int, failing []string) string {
	var sb strings.Builder
	sb.WriteString("\x1b[1m> monorepo@1.0.0 test\x1b[22m\n> pnpm -r --workspace-concurrency=4 test\n\n")
	suites := tests / 12
	for s := 0; s < suites; s++ {
		fmt.Fprintf(&sb, "\x1b[0m\x1b[7m\x1b[1m\x1b[32m PASS \x1b[39m\x1b[22m\x1b[27m\x1b[0m \x1b[2mpackages/%s/src/%s.test.ts\x1b[22m\n", pkgs[s%8], names[s%10])
		for t := 0; t < 12; t++ {
			fmt.Fprintf(&sb, "  %s\n    \x1b[32m✓\x1b[39m \x1b[2mhandles %s case %d (%d ms)\x1b[22m\n", names[(s+t)%10], names[(s*3+t)%10], t, 1+r.IntN(40))
		}
	}
	for _, f := range failing {
		fmt.Fprintf(&sb, "\x1b[0m\x1b[7m\x1b[1m\x1b[31m FAIL \x1b[39m\x1b[22m\x1b[27m\x1b[0m \x1b[2mpackages/billing/src/%s.test.ts\x1b[22m\n  \x1b[1m\x1b[31m● %s › retries the charge\x1b[39m\x1b[22m\n\n    expect(received).toEqual(expected)\n\n    Expected: 3\n    Received: 2\n\n", f, f)
		for i := 0; i < 9; i++ {
			fmt.Fprintf(&sb, "      at Object.<anonymous> (packages/billing/src/%s.ts:%d:%d)\n      at processTicksAndRejections (node:internal/process/task_queues:95:5)\n", names[i%10], 40+i*7, 11+i)
		}
		sb.WriteString("\n")
	}
	fmt.Fprintf(&sb, "\nTest Suites: %d failed, %d passed, %d total\nTests:       %d failed, %d passed, %d total\nTime:        %.3f s\n", len(failing), suites-len(failing), suites, len(failing), tests-len(failing), tests, 20+r.Float64()*25)
	return sb.String()
}

// dockerBuildLog is a BuildKit log. Cached steps repeat verbatim between rebuilds; the failing step differs.
func dockerBuildLog(r *rand.Rand, failAt int, round int) string {
	var sb strings.Builder
	sb.WriteString("#0 building with \"default\" instance using docker driver\n\n#1 [internal] load build definition from Dockerfile\n#1 transferring dockerfile: 1.2kB done\n#1 DONE 0.0s\n\n")
	for step := 2; step <= 9; step++ {
		fmt.Fprintf(&sb, "#%d [%d/9] RUN step-%d\n", step, step-1, step)
		if step < failAt {
			fmt.Fprintf(&sb, "#%d CACHED\n\n", step)
			continue
		}
		for i := 0; i < 70; i++ {
			fmt.Fprintf(&sb, "#%d %d.%03d npm http fetch GET 200 https://registry.npmjs.org/%s-%d 31ms (cache %s)\n", step, i/10, r.IntN(1000), names[i%10], i, []string{"hit", "miss", "stale"}[i%3])
		}
		if step == failAt && round < 3 {
			fmt.Fprintf(&sb, "#%d ERROR: process \"/bin/sh -c npm ci\" did not complete successfully: exit code: 1\nnpm ERR! code ERESOLVE\nnpm ERR! ERESOLVE could not resolve dependency %d\n", step, round)
			break
		}
		fmt.Fprintf(&sb, "#%d DONE %d.%ds\n\n", step, 3+r.IntN(40), r.IntN(10))
	}
	return sb.String()
}

// incidentLogs is an application log where one failing request path throws the same exception again and again, and
// a health check line repeats between real requests.
func incidentLogs(r *rand.Rand, n int) string {
	var sb strings.Builder
	trace := "java.lang.IllegalStateException: connection pool exhausted\n"
	for i := 0; i < 14; i++ {
		trace += fmt.Sprintf("\tat com.shop.%s.%s.handle(%s.java:%d)\n", pkgs[i%8], names[i%10], names[i%10], 40+i*11)
	}
	for i := 0; i < n; i++ {
		switch {
		case i%9 == 0:
			sb.WriteString("GET /healthz 200 1ms\nGET /healthz 200 1ms\nGET /healthz 200 1ms\nGET /healthz 200 1ms\n")
		case i%5 == 0:
			fmt.Fprintf(&sb, "ERROR checkout failed request=%08x\n%s", r.Uint32(), trace)
		default:
			fmt.Fprintf(&sb, "INFO  order accepted id=%d total=%d.%02d latency=%dms\n", 90000+i, r.IntN(300), r.IntN(100), 5+r.IntN(400))
		}
	}
	return sb.String()
}

func podStatus(restarts int) string {
	var sb strings.Builder
	sb.WriteString("NAME                           READY   STATUS             RESTARTS   AGE\n")
	for i := 0; i < 22; i++ {
		st, rs := "Running", 0
		if i == 3 {
			st, rs = "CrashLoopBackOff", restarts
		}
		fmt.Fprintf(&sb, "checkout-%s-7d9f8c6b5-%04x   1/1     %-18s %-10d %dh\n", names[i%10], 0x3a10+i, st, rs, 5+i)
	}
	return sb.String()
}

func wfCITriage(b *wfBuilder) {
	b.bash("pnpm -r test", verboseJest(b.r, 520, []string{"retry", "refund", "ledger", "invoice"}))
	b.read("packages/billing/src/retry.test.ts", 180)
	b.read("packages/billing/src/retry.ts", 160)
	b.grep("maxAttempts", 11)
	b.edit("packages/billing/src/retry.ts")
	b.bash("pnpm -r test", verboseJest(b.r, 520, []string{"refund", "ledger", "invoice"}))
	b.read("packages/billing/src/refund.ts", 210)
	b.edit("packages/billing/src/refund.ts")
	b.bash("pnpm -r test", verboseJest(b.r, 520, []string{"ledger"}))
	b.read("packages/billing/src/ledger.ts", 190)
	b.edit("packages/billing/src/ledger.ts")
	b.bash("pnpm -r test", verboseJest(b.r, 520, nil))
	b.bash("git diff", gitDiff(14))
	b.bash("git status", gitStatus(4))
	b.bash("pnpm -r test", verboseJest(b.r, 520, nil)) // a last confirmation run before committing
}

func wfContainerBuild(b *wfBuilder) {
	b.read("Dockerfile", 70)
	b.bash("docker build -t shop:dev .", dockerBuildLog(b.r, 5, 1))
	b.grep("ERESOLVE", 6)
	b.read("package.json", 60)
	b.edit("package.json")
	b.bash("docker build -t shop:dev .", dockerBuildLog(b.r, 5, 2))
	b.edit("Dockerfile")
	b.bash("docker build -t shop:dev .", dockerBuildLog(b.r, 5, 3))
	b.bash("docker run --rm shop:dev node server.js", logLines(b.r, 120, true))
	b.bash("docker logs shop-dev", logLines(b.r, 300, false))
	b.read("Dockerfile", 70)
	b.edit("Dockerfile")
	b.bash("docker build -t shop:dev .", dockerBuildLog(b.r, 9, 3))
	b.bash("docker images", strings.Repeat("shop   dev   3f2a91c0b4d1   2 minutes ago   412MB\n", 20))
}

func wfIncident(b *wfBuilder) {
	b.bash("kubectl get pods -n checkout", podStatus(7))
	b.bash("kubectl logs checkout-service-7d9f8c6b5-3a13 --tail=1500", incidentLogs(b.r, 520))
	b.grep("connection pool", 8)
	b.read("src/jobs/pool.java", 240)
	b.bash("kubectl get pods -n checkout", podStatus(7))
	b.bash("kubectl logs checkout-service-7d9f8c6b5-3a13 --tail=1500", incidentLogs(b.r, 520))
	b.edit("src/jobs/pool.java")
	b.bash("kubectl rollout restart deploy/checkout-service -n checkout", "deployment.apps/checkout-service restarted\n")
	b.bash("kubectl get pods -n checkout", podStatus(7))
	b.bash("kubectl logs checkout-service-7d9f8c6b5-3a13 --tail=1500", incidentLogs(b.r, 520))
	b.bash("kubectl get pods -n checkout", podStatus(0))
	b.bash("kubectl get pods -n checkout", podStatus(0))
	b.bash("kubectl logs checkout-service-7d9f8c6b5-3a13 --since=10m", incidentLogs(b.r, 260))
}
