// Package stress is `trove bench stress`: a five-layer stress suite that runs the real optimizer engine and
// gateway offline, with no provider call and no spend.
//
// Layers:
//
//  1. realism: seeded random sessions across the parameter space plus named edge cases. The only layer that
//     reports estimated savings.
//  2. fuzz: hostile and malformed inputs against every optimizer. Pass/fail only.
//  3. invariants: checks that must hold on every request any layer produces. Pass/fail only.
//  4. load: concurrent sessions, latency, memory and goroutine growth, SQLite contention, a configurable soak.
//  5. faults: upstream errors, corrupted stores, hanging hooks. Every case must fail open.
//
// Layers 2 to 5 never report savings. Every figure that is not a pass/fail count is labeled measured or
// estimated (CLAUDE.md rule 6). All data lives in a fresh temporary directory that is removed at the end, and
// the run refuses to use the real Trove data folder (CLAUDE.md rule 1 and the local-first rule).
package stress

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// Mode selects the sample sizes and durations.
type Mode string

// The two run modes.
const (
	ModeSmoke Mode = "smoke" // for CI: well under a minute
	ModeFull  Mode = "full"  // for nightly: thousands of samples, a longer soak
)

// Layer names, in run order.
const (
	LayerRealism    = "realism"
	LayerFuzz       = "fuzz"
	LayerInvariants = "invariants"
	LayerLoad       = "load"
	LayerFaults     = "faults"
)

// AllLayers is the run order.
var AllLayers = []string{LayerRealism, LayerFuzz, LayerInvariants, LayerLoad, LayerFaults}

// Options configures one run.
type Options struct {
	Mode   Mode
	Seed   uint64
	Layers []string      // empty means all five
	Soak   time.Duration // 0 uses the mode default
	// Samples overrides the realism sample count (0 uses the mode default).
	Samples int
	// ReplayDir is an optional folder of Claude Code session logs. Replay is not implemented in this build;
	// the realism layer reports it as skipped rather than pretending it ran.
	ReplayDir string
	// Now is for tests; nil uses time.Now.
	Now func() time.Time
	// skipEdges leaves out the fixed edge-case sessions of the realism layer. Tests set it to keep a run short;
	// the command never does, so a user's run always includes them.
	skipEdges bool
}

// Status is the outcome of one check or layer. A skipped layer is never counted as a pass.
type Status string

// The possible outcomes of a check or a layer.
const (
	Pass    Status = "pass"
	Fail    Status = "fail"
	Skipped Status = "skipped"
)

// InputInfo describes a failing input without its content: size and a short hash only (rule 7).
type InputInfo struct {
	Bytes int    `json:"bytes"`
	Hash  string `json:"hash"`
}

// Failure is one failed check, with enough to reproduce it.
type Failure struct {
	Layer     string    `json:"layer"`
	Case      string    `json:"case"`
	Invariant string    `json:"invariant"`
	Detail    string    `json:"detail,omitempty"`
	Input     InputInfo `json:"input"`
	Repro     string    `json:"repro"`
}

// Savings is the estimated saving from the realism layer. Only that layer has this field, which is how the
// rule "no savings claims from layers 2 to 5" is enforced structurally.
type Savings struct {
	Label   string        `json:"label"` // always "estimated"
	Samples int           `json:"samples"`
	Overall SavingSummary `json:"overall"`
	Drivers []Driver      `json:"drivers"`
	Edges   []EdgeResult  `json:"edge_cases"`
	ByModel []GroupSaving `json:"by_model"`
	Pricing string        `json:"pricing_date"`
	Notes   []string      `json:"notes,omitempty"`
	// Replay is the aggregate of local session logs, when --replay-dir is given. It is counts and bytes only.
	Replay *ReplaySummary `json:"replay,omitempty"`
}

// replayPerSession is how many requests are sampled from each session log.
func replayPerSession(m Mode) int {
	if m == ModeFull {
		return 16
	}
	return 8
}

// SavingSummary is the saving across all samples, as a percent of the off-arm list-price cost.
type SavingSummary struct {
	MedianPct float64 `json:"median_pct"`
	P10Pct    float64 `json:"p10_pct"`
	P90Pct    float64 `json:"p90_pct"`
}

// Driver says how strongly one parameter moves the estimated saving.
type Driver struct {
	Parameter string  `json:"parameter"`
	Kind      string  `json:"kind"`     // "numeric" (Spearman rho) or "categorical" (spread of group medians)
	Strength  float64 `json:"strength"` // |rho| or the spread in percentage points
	// Noise is the 95th percentile of the same statistic after the savings are shuffled across sessions. A
	// parameter whose strength does not clear its noise is reported as not distinguishable from noise.
	Noise    float64 `json:"noise_p95"`
	Distinct bool    `json:"distinct_from_noise"`
	Rank     int     `json:"rank"`
}

// EdgeResult is one named edge case.
type EdgeResult struct {
	Name     string  `json:"name"`
	Turns    int     `json:"turns"`
	SavedPct float64 `json:"estimated_saved_pct"`
	Passed   bool    `json:"invariants_passed"`
}

// GroupSaving is the median saving within one model group.
type GroupSaving struct {
	Model     string  `json:"model"`
	N         int     `json:"n"`
	MedianPct float64 `json:"median_pct"`
}

// LayerResult is one layer's outcome.
type LayerResult struct {
	Name     string    `json:"name"`
	Status   Status    `json:"status"`
	Cases    int       `json:"cases"`
	Passed   int       `json:"passed"`
	Failed   int       `json:"failed"`
	Skipped  int       `json:"skipped"` // checks that could not run here; never counted as passed
	Took     string    `json:"took"`
	Notes    []string  `json:"notes,omitempty"`
	Failures []Failure `json:"failures,omitempty"`
	// Measured holds non-savings measurements (latency, memory). Each entry says what it is.
	Measured []Measure `json:"measured,omitempty"`
	// Savings is set only for the realism layer.
	Savings *Savings `json:"savings,omitempty"`
}

// Measure is a labeled number that is not a saving: a latency, a heap size.
type Measure struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
	Label string  `json:"label"` // "measured"
}

// Report is the whole run.
type Report struct {
	Mode     Mode          `json:"mode"`
	Seed     uint64        `json:"seed"`
	GoOS     string        `json:"goos"`
	GoArch   string        `json:"goarch"`
	GoVer    string        `json:"go_version"`
	Started  string        `json:"started"`
	Elapsed  string        `json:"elapsed"`
	Status   Status        `json:"status"`
	Layers   []LayerResult `json:"layers"`
	Failures int           `json:"failure_count"`
	// Checks totals the case counts across layers. Skipped checks are shown in the headline and never counted as
	// passes. A run where a selected layer was skipped entirely has status fail.
	Checks Totals `json:"checks"`
}

// SkippedLayers names the layers that ran no checks at all. Each is a failure of the run, because an expected
// layer that does not run proves nothing.
func SkippedLayers(r Report) []string {
	var out []string
	for _, l := range r.Layers {
		if l.Status == Skipped {
			out = append(out, l.Name)
		}
	}
	return out
}

// Totals counts checks across all layers.
type Totals struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// Params returns the defaults for a mode.
type params struct {
	samples      int
	fuzzVariants int
	concurrency  int
	loadDur      time.Duration
	soak         time.Duration
	capPerCase   time.Duration
}

func paramsFor(m Mode) params {
	if m == ModeFull {
		return params{samples: 2000, fuzzVariants: 60, concurrency: 64, loadDur: 60 * time.Second,
			soak: 15 * time.Minute, capPerCase: 10 * time.Second}
	}
	return params{samples: 40, fuzzVariants: 1, concurrency: 8, loadDur: 2 * time.Second,
		soak: 5 * time.Second, capPerCase: 5 * time.Second}
}

// Run executes the selected layers and returns the report. It never returns a partial result without saying
// which layer did not run.
func Run(o Options) (Report, error) {
	if o.Mode == "" {
		o.Mode = ModeSmoke
	}
	if o.Mode != ModeSmoke && o.Mode != ModeFull {
		return Report{}, fmt.Errorf("unknown mode %q: use smoke or full", o.Mode)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	p := paramsFor(o.Mode)
	if o.Samples > 0 {
		p.samples = o.Samples
	}
	if o.Soak > 0 {
		p.soak = o.Soak
	}
	layers := o.Layers
	if len(layers) == 0 {
		layers = AllLayers
	}
	for _, l := range layers {
		if !validLayer(l) {
			return Report{}, fmt.Errorf("unknown layer %q: use one or more of %v", l, AllLayers)
		}
	}

	dir, err := scratchDir()
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	start := o.Now()
	rep := Report{Mode: o.Mode, Seed: o.Seed, GoOS: runtime.GOOS, GoArch: runtime.GOARCH,
		GoVer: runtime.Version(), Started: start.UTC().Format(time.RFC3339)}
	env := &env{opts: o, p: p, dir: dir}
	for _, name := range AllLayers {
		if !contains(layers, name) {
			continue
		}
		var lr LayerResult
		t0 := time.Now()
		switch name {
		case LayerRealism:
			lr = env.realism()
		case LayerFuzz:
			lr = env.fuzz()
		case LayerInvariants:
			lr = env.invariants()
		case LayerLoad:
			lr = env.load()
		case LayerFaults:
			lr = env.faults()
		}
		lr.Took = time.Since(t0).Round(10 * time.Millisecond).String()
		rep.Layers = append(rep.Layers, lr)
		rep.Failures += len(lr.Failures)
	}
	rep.Elapsed = o.Now().Sub(start).Round(time.Millisecond).String()
	rep.Status = Pass
	for _, l := range rep.Layers {
		rep.Checks.Passed += l.Passed
		rep.Checks.Failed += l.Failed
		rep.Checks.Skipped += l.Skipped
		if l.Status == Fail || l.Status == Skipped {
			rep.Status = Fail
		}
	}
	return rep, nil
}

// env carries what every layer needs: options, sizes, and a scratch folder.
type env struct {
	opts Options
	p    params
	dir  string
}

func (e *env) repro(layer string) string {
	return fmt.Sprintf("trove bench stress --mode %s --seed %d --layers %s", e.opts.Mode, e.opts.Seed, layer)
}

// record counts one case in a layer. A case with no violations passes. A case with violations fails once,
// and each violation is listed so the report says exactly which rule broke on which input.
func (e *env) record(lr *LayerResult, c string, vs []Violation, input []byte) {
	lr.Cases++
	if len(vs) == 0 {
		lr.Passed++
		return
	}
	lr.Failed++
	for _, v := range vs {
		lr.Failures = append(lr.Failures, Failure{Layer: lr.Name, Case: c, Invariant: v.Invariant, Detail: v.Detail,
			Input: info(input), Repro: e.repro(lr.Name)})
	}
}

// finish sets a layer's status. Any failure fails the layer. A layer where nothing ran is skipped. A layer
// that ran but had skipped checks still passes, and the skipped checks stay listed in its notes.
func finish(lr *LayerResult) {
	switch {
	case lr.Failed > 0:
		lr.Status = Fail
	case lr.Passed == 0 && lr.Cases == 0:
		lr.Status = Skipped
	default:
		lr.Status = Pass
	}
}

// scratchDir makes a fresh temporary folder for this run. It refuses the real Trove data folder.
func scratchDir() (string, error) {
	realDir, err := config.ResolveDataDir("", os.Getenv)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "trove-stress-")
	if err != nil {
		return "", fmt.Errorf("could not create a scratch folder for the stress run: %w", err)
	}
	if sameDir(dir, realDir) {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("the scratch folder resolved to your Trove data folder (%s); refusing to run", realDir)
	}
	return dir, nil
}

func sameDir(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

func validLayer(l string) bool { return contains(AllLayers, l) }

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
