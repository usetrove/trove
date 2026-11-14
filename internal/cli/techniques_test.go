package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTechniquesListsEverythingAndStaysOutOfTheMainHelp(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("techniques"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	for _, want := range []string{"B1", "B15", "C13", "W1", "built", "candidate", "excluded", "trove techniques check"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("list lacks %q:\n%s", want, h.out)
		}
	}
	h = newHarness(t, nil)
	h.run("techniques", "--json")
	var out struct {
		Techniques []struct {
			ID           string   `json:"id"`
			Status       string   `json:"status"`
			Names        []string `json:"names"`
			Gateway      bool     `json:"needs_gateway"`
			Subscription bool     `json:"works_for_subscription"`
		} `json:"techniques"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil || len(out.Techniques) < 30 {
		t.Fatalf("--json: %v, %d entries", err, len(out.Techniques))
	}
	for _, x := range out.Techniques {
		if x.Gateway && x.Subscription {
			t.Errorf("%s says it needs the gateway and works for a subscription", x.ID)
		}
	}
	// A hidden command: not in the main help, but listed by `trove advanced`.
	h = newHarness(t, nil)
	h.run("--help")
	if strings.Contains(h.out.String(), "techniques") {
		t.Errorf("techniques shows in the main help:\n%s", h.out)
	}
	h = newHarness(t, nil)
	h.run("advanced")
	if !strings.Contains(h.out.String(), "techniques") {
		t.Errorf("advanced does not list techniques:\n%s", h.out)
	}
}

func TestTechniquesCheckExplainsConflictsAndMissingPieces(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("techniques", "check", "rtk", "noise_normalizer", "--login", "subscription"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), "cannot all be on together") || !strings.Contains(h.out.String(), "both shorten the same shell output") {
		t.Errorf("output:\n%s", h.out)
	}

	h = newHarness(t, nil)
	h.run("techniques", "check", "noise_normalizer", "hook_repeat_read", "--login", "subscription", "--hook-probe", "Bash:output")
	if !strings.Contains(h.out.String(), "can all be on together") {
		t.Errorf("compatible set was refused:\n%s", h.out)
	}

	h = newHarness(t, nil)
	h.run("techniques", "check", "duplicate_result_collapser", "--login", "subscription", "--json")
	var out struct {
		OK      bool     `json:"ok"`
		Reasons []string `json:"reasons"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil || out.OK || len(out.Reasons) == 0 || !strings.Contains(out.Reasons[0], "never touches a subscription login") {
		t.Errorf("--json: %v %+v", err, out)
	}

	h = newHarness(t, nil)
	h.run("techniques", "check", "no-such-thing")
	if !strings.Contains(h.out.String(), "not a technique Trove knows about") {
		t.Errorf("unknown name:\n%s", h.out)
	}
}

func TestTechniquesCheckRejectsABadLoginWithAnExplanation(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("techniques", "check", "rtk", "--login", "free"); code != 1 || !strings.Contains(h.err.String(), "use api_key, subscription or unknown") {
		t.Errorf("exit %d, stderr %q", code, h.err)
	}
}

// A hook technique needs a probe that saw its rewrite work. Without --hook-probe the check says so and says what
// to run; with the right one it passes, and a malformed value is refused with an example.
func TestTechniquesCheckKnowsAboutHookProbes(t *testing.T) {
	h := newHarness(t, nil)
	h.run("techniques", "check", "noise_normalizer", "--login", "subscription")
	if !strings.Contains(h.out.String(), "Bash output rewriting") || !strings.Contains(h.out.String(), "trove hooks verify") {
		t.Errorf("without a probe:\n%s", h.out)
	}
	h = newHarness(t, nil)
	h.run("techniques", "check", "noise_normalizer", "--login", "subscription", "--hook-probe", "Bash:output")
	if !strings.Contains(h.out.String(), "can all be on together") {
		t.Errorf("with the probe:\n%s", h.out)
	}
	// The gateway makes the probe unnecessary for a technique that also runs there.
	h = newHarness(t, nil)
	h.run("techniques", "check", "noise_normalizer", "--login", "api_key", "--gateway")
	if !strings.Contains(h.out.String(), "can all be on together") {
		t.Errorf("with the gateway:\n%s", h.out)
	}
	h = newHarness(t, nil)
	if code := h.run("techniques", "check", "noise_normalizer", "--hook-probe", "Bash"); code != 1 || !strings.Contains(h.err.String(), "Bash:output") {
		t.Errorf("exit %d, stderr %q", code, h.err)
	}
}
