package stress

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// Layers 2 to 5 must never carry savings. This is the rule from the task, checked on the JSON a user sees.
func TestOnlyRealismCarriesSavings(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the invariants and faults layers in full (about 25 s, ten times that under -race); the nightly workflow runs it")
	}
	rep, err := Run(Options{Mode: ModeSmoke, Seed: 1, Layers: []string{LayerInvariants, LayerFaults}})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range rep.Layers {
		if l.Savings != nil {
			t.Fatalf("layer %s carries a savings block; only realism may", l.Name)
		}
		b, _ := json.Marshal(l)
		if strings.Contains(string(b), `"savings"`) || strings.Contains(string(b), "estimated_saved") {
			t.Fatalf("layer %s emits saving fields", l.Name)
		}
	}
}

// Same seed, same numbers. A short realism run is enough to show that the sampler and engine are deterministic.
func TestRealismIsDeterministicForASeed(t *testing.T) {
	samples := 6
	if testing.Short() || raceEnabled {
		samples = 2 // two runs of the same seed are still enough to show determinism; each sample is slow under -race
	}
	run := func() string {
		rep, err := Run(Options{Mode: ModeSmoke, Seed: 20261108, Layers: []string{LayerRealism}, Samples: samples, skipEdges: true})
		if err != nil {
			t.Fatal(err)
		}
		rep.Layers[0].Took = ""
		b, _ := json.Marshal(rep.Layers[0].Savings)
		return string(b)
	}
	if a, b := run(), run(); a != b {
		t.Fatalf("the same seed gave different savings:\n%s\n%s", a, b)
	}
}

// A skipped check is never a pass.
func TestSkippedIsNeverAPass(t *testing.T) {
	lr := LayerResult{Name: "x"}
	finish(&lr)
	if lr.Status != Skipped {
		t.Fatalf("a layer with nothing run is %s, want skipped", lr.Status)
	}
	lr = LayerResult{Name: "x", Cases: 1, Passed: 1, Skipped: 3}
	finish(&lr)
	if lr.Status != Pass {
		t.Fatalf("a layer whose run passed is %s, want pass", lr.Status)
	}
	lr = LayerResult{Name: "x", Cases: 2, Passed: 1, Failed: 1}
	finish(&lr)
	if lr.Status != Fail {
		t.Fatalf("a failing layer is %s, want fail", lr.Status)
	}
}

// The invariant layer must pass on the shipped engine: every control caught, determinism holds.
func TestInvariantLayerPasses(t *testing.T) {
	e := &env{opts: Options{Mode: ModeSmoke, Seed: 7}, p: paramsFor(ModeSmoke)}
	lr := e.invariants()
	if lr.Status != Pass {
		t.Fatalf("invariant layer: %s; failures: %+v", lr.Status, lr.Failures)
	}
}

func TestSpearmanOnMonotoneData(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5, 6}
	ys := []float64{2, 4, 5, 9, 10, 30}
	if r := spearman(xs, ys); math.Abs(r-1) > 1e-9 {
		t.Fatalf("spearman of a monotone pair = %v, want 1", r)
	}
	if r := spearman(xs, []float64{6, 5, 4, 3, 2, 1}); math.Abs(r+1) > 1e-9 {
		t.Fatalf("spearman of an inverse pair = %v, want -1", r)
	}
}

func TestHeapSlopeFitsALine(t *testing.T) {
	var pts []heapPoint
	for i := 0; i < 50; i++ {
		pts = append(pts, heapPoint{t: float64(i), mb: 100 + 3*float64(i)})
	}
	if s := heapSlope(pts); math.Abs(s-3) > 1e-9 {
		t.Fatalf("slope = %v, want 3", s)
	}
	if s := heapSlope(pts[:2]); s != 0 {
		t.Fatalf("too few samples should give 0, got %v", s)
	}
}

// The corruption checks must see the shape of a request, not only its bytes.
func TestCheckRequestCatchesDroppedResult(t *testing.T) {
	base := envelope(jstr("ok"), "")
	broken := strings.Replace(string(base), `"tool_use_id":"toolu_1"`, `"tool_use_id":"toolu_9"`, 1)
	if !hasInvariant(CheckRequest(base, []byte(broken), 0), InvPairing) {
		t.Fatal("a changed tool_use_id was not reported")
	}
}

func TestPercentilesAreOrdered(t *testing.T) {
	var ds []time.Duration
	for i := 1; i <= 100; i++ {
		ds = append(ds, time.Duration(i)*time.Millisecond)
	}
	p50, p99 := percentiles(ds)
	if !(p50 < p99) || p50 < 40 || p50 > 60 {
		t.Fatalf("p50=%v p99=%v", p50, p99)
	}
}

// The scratch folder must never be the real data folder.
func TestScratchFolderIsNotTheDataFolder(t *testing.T) {
	dir, err := scratchDir()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if !strings.Contains(dir, "trove-stress-") {
		t.Fatalf("unexpected scratch folder %q", dir)
	}
}
