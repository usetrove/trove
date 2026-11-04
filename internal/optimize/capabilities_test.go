package optimize

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateDocs = flag.Bool("update", false, "rewrite the generated part of docs/adapters.md")

const (
	docBegin = "<!-- BEGIN GENERATED: capability matrix (go test ./internal/optimize -run TestAdaptersDoc -update) -->"
	docEnd   = "<!-- END GENERATED -->"
)

func TestMatrixCoversEveryOptimizerAndAdapter(t *testing.T) {
	want := map[string]bool{}
	for _, o := range Pipeline() {
		want[o.Name()] = true
	}
	for _, o := range ToolPipeline() {
		want[o.Name()] = true
	}
	rows := Matrix()
	if len(rows) != len(want) {
		t.Fatalf("matrix has %d rows for %d optimizers", len(rows), len(want))
	}
	for _, r := range rows {
		if !want[r.Optimizer] {
			t.Errorf("unexpected row %q", r.Optimizer)
		}
		for _, a := range Adapters {
			s, ok := r.Support[a]
			if !ok || s.Reason == "" || (s.Level != LevelSupported && s.Level != LevelPartial && s.Level != LevelImpossible) {
				t.Errorf("%s/%s has no level or no reason: %+v", r.Optimizer, a, s)
			}
		}
	}
}

func TestMatrixMatchesTheFacts(t *testing.T) {
	by := map[string]Row{}
	for _, r := range Matrix() {
		by[r.Optimizer] = r
	}
	for _, tc := range []struct {
		opt string
		a   Adapter
		l   Level
	}{
		{"duplicate_result_collapser", AdapterGateway, LevelSupported},
		{"duplicate_result_collapser", AdapterHooks, LevelImpossible},
		{"noise_normalizer", AdapterHooks, LevelPartial},
		{"hook_repeat_read", AdapterGateway, LevelImpossible},
		{"hook_repeat_read", AdapterHooks, LevelSupported},
	} {
		if got := by[tc.opt].Support[tc.a].Level; got != tc.l {
			t.Errorf("%s on %s = %s, want %s", tc.opt, tc.a, got, tc.l)
		}
	}
}

// The matrix in docs/adapters.md is generated; this fails when it drifts from code.
func TestAdaptersDocIsGenerated(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "adapters.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	doc := strings.ReplaceAll(string(b), "\r\n", "\n")
	i, j := strings.Index(doc, docBegin), strings.Index(doc, docEnd)
	if i < 0 || j < i {
		t.Fatalf("%s is missing the generated-section markers", path)
	}
	gen := docBegin + "\n\n" + RenderMarkdown(Matrix()) + "\n" + docEnd
	got := doc[:i] + gen + doc[j+len(docEnd):]
	if *updateDocs {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if got != doc {
		t.Fatal("docs/adapters.md is out of date with the optimizers' declarations; run: go test ./internal/optimize -run TestAdaptersDoc -update")
	}
}
