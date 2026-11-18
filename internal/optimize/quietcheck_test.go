package optimize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readQuietFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "quiet", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Every quiet rule that names a recorded fixture must keep every failure line of
// the tool's default output, and the fixture must contain some, or the check
// proves nothing.
func TestQuietRuleFixturesKeepEveryFailureLine(t *testing.T) {
	checked := 0
	for _, r := range QuietRules {
		if r.ErrorFixture == "" {
			continue
		}
		checked++
		full := readQuietFixture(t, r.ErrorFixture+".default.txt")
		quiet := readQuietFixture(t, r.ErrorFixture+".quiet.txt")
		if n := len(FailureLines(full)); n < 3 {
			t.Errorf("%v: the default-output fixture has %d failure lines; record one with failing tests", r.Prefix, n)
		}
		if miss := MissingFailureLines(full, quiet); len(miss) > 0 {
			t.Errorf("%v: quiet output drops failure lines: %q", r.Prefix, miss)
		}
		if r.ErrorCheckedAt == "" {
			t.Errorf("%v: a fixture needs the date and tool version it was recorded with", r.Prefix)
		}
	}
	if checked == 0 {
		t.Fatal("no quiet rule has an error-visibility fixture")
	}
}

func TestMissingFailureLinesCatchesADroppedError(t *testing.T) {
	full := readQuietFixture(t, "pytest.default.txt")
	quiet := readQuietFixture(t, "pytest.quiet.txt")
	cut := strings.Replace(quiet, "E       ValueError: invalid literal for int() with base 10: 'not a number'\n", "", 1)
	if cut == quiet {
		t.Fatal("fixture changed; update the line this test removes")
	}
	miss := MissingFailureLines(full, cut)
	if len(miss) != 1 || !strings.Contains(miss[0], "ValueError") {
		t.Fatalf("missing = %q", miss)
	}
}

// Cargo cannot be run where this was written, so its rules have no fixture and
// must stay estimate-only.
func TestRulesWithoutEvidenceStayShadow(t *testing.T) {
	for _, r := range QuietRules {
		if r.Prefix[0] == "cargo" && (r.Verified || r.ErrorFixture != "") {
			t.Errorf("%v has no recorded evidence that errors stay visible", r.Prefix)
		}
	}
}
