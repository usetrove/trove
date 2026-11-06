package recommend

import (
	"os"
	"testing"
)

// The benchmark lab generates defaults.toml (python -m trovebench defaults). This test loads a
// generated file through the real loader so a schema drift between the generator and Go fails
// here, not in a user's `trove recommend`. It runs only when TROVE_GENERATED_DEFAULTS names a file.
func TestGeneratedDefaultsLoad(t *testing.T) {
	path := os.Getenv("TROVE_GENERATED_DEFAULTS")
	if path == "" {
		t.Skip("set TROVE_GENERATED_DEFAULTS to a generated defaults file to check it")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := LoadDefaults(b)
	if err != nil {
		t.Fatalf("generated defaults do not load: %v", err)
	}
	if len(d.Rules) == 0 {
		t.Fatal("generated defaults hold no rules")
	}
	var bench int
	for _, r := range d.Rules {
		if r.Evidence == EvidenceBenchmark {
			bench++
			if r.Source == "" {
				t.Errorf("rule %q is benchmark evidence without a source", r.ID)
			}
		}
	}
	t.Logf("release %q: %d rules, %d backed by benchmark evidence", d.Release, len(d.Rules), bench)
}
