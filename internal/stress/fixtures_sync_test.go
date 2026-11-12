package stress

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The permanent regression fixtures in internal/optimize/testdata must stay the same bytes as the failing fuzz
// inputs they came from. If the corpus changes, regenerate the fixtures on purpose; do not let them drift.
func TestRegressionFixturesMatchFuzzCorpus(t *testing.T) {
	for _, c := range corpusFamilies(20261108, 0) {
		if c.family != "binary-invalid-utf8" && c.family != "deep-nesting" {
			continue
		}
		got, err := os.ReadFile(filepath.Join("..", "optimize", "testdata", "invalid-json", c.family+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, c.body) {
			t.Fatalf("fixture %s no longer matches the fuzz corpus", c.family)
		}
	}
}
