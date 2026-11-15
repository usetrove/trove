package stress

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"net/http"
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

// Headline is the three figures worth quoting, each with its label and its limits.
type Headline struct {
	MedianSavedPct   float64 `json:"median_saved_pct"` // estimated
	MinSavedPct      float64 `json:"min_saved_pct"`
	MaxSavedPct      float64 `json:"max_saved_pct"`
	Requests         int     `json:"requests"`
	Violations       int     `json:"violations"`        // measured
	PrefixPreserved  int     `json:"prefix_preserved"`  // measured
	AllDeterministic bool    `json:"all_deterministic"` // measured
	EngineP50ms      float64 `json:"engine_p50_ms"`     // measured, engine only
	EngineP99ms      float64 `json:"engine_p99_ms"`     // measured, engine only
}

// WorkflowOptions configures a run.
type WorkflowOptions struct {
	Seed  uint64
	Model string // default claude-sonnet-5-5
}

const workflowPrefixTokens = 18000 // Claude Code's system prompt, tool definitions and project context, roughly

type wfSpec struct {
	name, desc string
	build      func(b *wfBuilder)
}

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
	saved := make([]float64, 0, len(rep.Rows))
	h := Headline{AllDeterministic: true}
	for _, r := range rep.Rows {
		saved = append(saved, r.SavedPct)
		h.Requests += r.Requests
		h.Violations += r.Violations
		h.PrefixPreserved += r.PrefixOK
		h.AllDeterministic = h.AllDeterministic && r.Deterministic
	}
	sort.Float64s(saved)
	h.MedianSavedPct, h.MinSavedPct, h.MaxSavedPct = round1(quantile(saved, 0.5)), round1(saved[0]), round1(saved[len(saved)-1])
	sort.Float64s(lat)
	h.EngineP50ms, h.EngineP99ms = round2(quantile(lat, 0.5)), round2(quantile(lat, 0.99))
	rep.Headline = h
	rep.Notes = []string{
		"The workflows are scripted from typical Claude Code patterns. How often a file is re-read, how noisy a shell is and how long a session runs are assumptions written in internal/stress/workflows.go, not measurements of real users.",
		"Costs are estimates at list price on the pinned model and the pricing date shown. Token counts use the engine's bytes-per-token ratio, not provider counts. Declared optimizer overhead is charged to the optimized arm.",
		"The cache model is the realism layer's: the cached prefix is what matches the previous request byte for byte, and each turn has a 10% chance of a cold cache in both arms.",
		"Engine timings are measured in-process on this machine and exclude the gateway's own forwarding time.",
		"Savings from these sessions say what the optimizers do on these patterns. They are not a forecast of any user's bill.",
	}
	return rep, nil
}

func runWorkflow(w wfSpec, o WorkflowOptions, tab *pricing.Table) (WorkflowRow, []float64, error) {
	turns := buildWorkflow(w, o.Seed)
	spec := SessionSpec{Name: "wf-" + w.name, PrefixTokens: workflowPrefixTokens, Model: o.Model, Turns: len(turns)}
	bodies := buildBodies(spec, turns)

	first, lat, row := playWorkflow(spec, bodies, turns, o, tab)
	second, _, _ := playWorkflow(spec, bodies, turns, o, tab)
	row.Name, row.Description, row.Turns = w.name, w.desc, len(turns)
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
	return outs, lat, row
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
		{"quick-question", "Three turns: read a file, search, answer. The case where little can help.", wfQuick},
		{"fix-failing-test", "pytest fails, read the test and source, edit, rerun twice until green.", wfFixTest},
		{"debug-with-logs", "Run the app, read noisy colored logs, search them, fix, restart and read the same startup noise again.", wfDebugLogs},
		{"dependency-upgrade", "npm install with progress noise, build warnings that repeat, CRLF test output, fix, rebuild.", wfUpgrade},
		{"rename-across-codebase", "Search every usage, read and edit many files, type-check, re-read, fix, type-check again.", wfRename},
		{"explore-and-review", "Orient in an unfamiliar repo: many reads, repeated status and diff, a second pass over key files.", wfExplore},
		{"long-feature-session", "About 90 turns: explore, implement across files, test, debug, refactor. A normal long day.", wfLong},
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
	fmt.Fprintf(&sb, "# Scripted workflows: what the optimizers do on everyday Claude Code sessions\n\n")
	fmt.Fprintf(&sb, "Model %s, list prices as of %s, seed %d. Scripted sessions, not real users: see the limits at the end.\n\n", r.Model, r.Pricing, r.Seed)
	sb.WriteString("## Three numbers\n\n")
	fmt.Fprintf(&sb, "1. **Estimated saving: median %.1f%% of list-price cost across %d scripted workflows (range %.1f%% to %.1f%%).** Estimated. Depends on the scripted shape of each session.\n", h.MedianSavedPct, len(r.Rows), h.MinSavedPct, h.MaxSavedPct)
	fmt.Fprintf(&sb, "2. **Cache and correctness: %d of %d requests kept the cached prefix byte-identical, %d broken invariants, repeat runs byte-identical: %v.** Measured by the checkers in this run.\n", h.PrefixPreserved, h.Requests, h.Violations, h.AllDeterministic)
	fmt.Fprintf(&sb, "3. **Engine time per request: p50 %.2f ms, p99 %.2f ms.** Measured in-process on this machine; excludes the gateway's forwarding time.\n\n", h.EngineP50ms, h.EngineP99ms)
	sb.WriteString("## By workflow\n\n| workflow | turns | input tokens (est.) | tokens removed (est.) | overhead charged (tokens) | cost off (est.) | cost on (est.) | saving (est.) | prefix kept | violations |\n|---|---|---|---|---|---|---|---|---|---|\n")
	for _, w := range r.Rows {
		fmt.Fprintf(&sb, "| %s | %d | %d | %d | %d | $%.4f | $%.4f | %.1f%% | %d/%d | %d |\n", w.Name, w.Turns, w.InputTokens, w.TokensCut, w.OverheadToks, w.OffUSD, w.OnUSD, w.SavedPct, w.PrefixOK, w.Requests, w.Violations)
	}
	sb.WriteString("\n")
	for _, w := range r.Rows {
		fmt.Fprintf(&sb, "- **%s**: %s\n", w.Name, w.Description)
	}
	sb.WriteString("\n## Limits (say these when you quote the numbers)\n\n")
	for _, n := range r.Notes {
		fmt.Fprintf(&sb, "- %s\n", n)
	}
	return sb.String()
}
