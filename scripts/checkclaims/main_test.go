package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bench", "results"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bench", "results", "s.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, text string
		publish    bool
		want       string // substring of the single expected problem; "" for none
	}{
		{"no figures", "Trove runs locally.", false, ""},
		{"bare percent", "Saves 40% of tokens.", false, "outside"},
		{"bare dollars", "You spent $12.", false, "outside"},
		{"bare multiplier", "Runs 3x faster.", false, "outside"},
		{"bare token count", "Wasted 12k tokens.", false, "outside"},
		{"slot with real source", "Saves [MEASURED: median saving 40% — source: bench/results/s.json].", true, ""},
		{"slot missing source word", "[MEASURED: 40%]", false, "no `source:`"},
		{"slot source not a file", "[ESTIMATED: 5% — source: bench/results/nope.json]", false, "does not exist"},
		{"placeholder ok in draft", "[MEASURED: 40% — source: bench/results/<run>/s.json]", false, ""},
		{"placeholder fails publish", "[MEASURED: 40% — source: bench/results/<run>/s.json]", true, "unresolved"},
		{"code fences ignored", "```\ntrove --budget 50%\n```", false, ""},
		{"number without claim shape", "A 30 to 45 second demo of version 0.1.0.", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ps := check("d.md", tc.text, root, tc.publish)
			if tc.want == "" {
				if len(ps) != 0 {
					t.Fatalf("want no problems, got %v", ps)
				}
				return
			}
			if len(ps) != 1 || !strings.Contains(ps[0].msg, tc.want) {
				t.Fatalf("want one problem containing %q, got %v", tc.want, ps)
			}
		})
	}
}
