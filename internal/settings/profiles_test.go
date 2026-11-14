package settings

import (
	"strings"
	"testing"
)

func loadAll(t *testing.T) (*Catalog, *ProfileSet) {
	t.Helper()
	c, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	p, err := BuiltinProfiles(c)
	if err != nil {
		t.Fatalf("embedded profiles must validate: %v", err)
	}
	return c, p
}

func TestBuiltinProfiles(t *testing.T) {
	c, p := loadAll(t)
	want := []string{"exploration", "feature", "quick-fix", "refactor"}
	if strings.Join(p.Tasks(), ",") != strings.Join(want, ",") {
		t.Fatalf("tasks = %v, want %v", p.Tasks(), want)
	}
	// The shipped defaults must never lower anything without evidence: with
	// unproven values excluded, every remaining value is risk "none" or has
	// an evidence reference.
	for _, pr := range p.Profiles {
		for _, h := range []string{ClaudeCode, Codex} {
			for _, v := range pr.Select(c, h, false).Use {
				s, _ := c.Lookup(v.Harness, v.Key)
				if v.Evidence == EvidenceNone && s.Risk != RiskNone {
					t.Errorf("%s/%s ships %s without evidence", pr.Task, h, v.Key)
				}
			}
		}
	}
}

func TestSelectGatesUnproven(t *testing.T) {
	c, p := loadAll(t)
	qf, _ := p.Get("quick-fix")
	def := qf.Select(c, Codex, false)
	if len(def.Use) != 1 || def.Use[0].Key != "model_reasoning_summary" {
		t.Errorf("default codex quick-fix = %+v", def.Use)
	}
	if len(def.Skipped) != 1 || def.Skipped[0].Value.Key != "model_verbosity" {
		t.Errorf("skipped = %+v", def.Skipped)
	}
	all := qf.Select(c, Codex, true)
	if len(all.Use) != 2 || len(all.Skipped) != 0 {
		t.Errorf("with allow_unproven = %+v", all)
	}
	// Determinism: same inputs, same order.
	again := qf.Select(c, Codex, true)
	for i := range all.Use {
		if all.Use[i].Key != again.Use[i].Key {
			t.Fatal("selection order changed between calls")
		}
	}
}

func TestProfileValidationRejects(t *testing.T) {
	c, _ := loadAll(t)
	mk := func(extra string) string {
		return "version = 1\n[[profile]]\ntask = \"t\"\ndescription = \"d\"\n[[profile.value]]\nwhy = \"w\"\nevidence = \"none\"\n" + extra
	}
	tests := []struct{ name, body, want string }{
		{"unknown key", mk("harness = \"codex\"\nkey = \"nope\"\nvalue = \"x\""), "not in the codex settings catalog"},
		{"suggestion-only key", mk("harness = \"claude_code\"\nkey = \"MAX_THINKING_TOKENS\"\nvalue = \"2048\""), "cannot be applied"},
		{"unconfirmed value", mk("harness = \"codex\"\nkey = \"model_verbosity\"\nvalue = \"high\""), "not been confirmed"},
		{"out of range", mk("harness = \"claude_code\"\nkey = \"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE\"\nvalue = \"500\""), "outside the documented range"},
		{"not a number", mk("harness = \"claude_code\"\nkey = \"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE\"\nvalue = \"half\""), "not a whole number"},
		{"list for scalar", mk("harness = \"claude_code\"\nkey = \"outputStyle\"\nlist = [\"Concise\"]"), "single value"},
		{"scalar for list", mk("harness = \"claude_code\"\nkey = \"claudeMdExcludes\"\nvalue = \"x\""), "takes a list"},
		{"regressed", mk("harness = \"codex\"\nkey = \"model_verbosity\"\nvalue = \"low\"\nregressed = true"), "must not ship"},
		{"missing evidence", strings.Replace(mk("harness = \"codex\"\nkey = \"model_verbosity\"\nvalue = \"low\""), "evidence = \"none\"\n", "", 1), "needs an evidence"},
		{"future version", strings.Replace(mk(""), "version = 1", "version = 9", 1), "Upgrade Trove"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseProfiles([]byte(tc.body))
			if err == nil {
				err = p.Validate(c)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestValidProfileValues(t *testing.T) {
	c, _ := loadAll(t)
	p, err := ParseProfiles([]byte(`version = 1
[[profile]]
task = "t"
description = "d"
[[profile.value]]
harness = "claude_code"
key = "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE"
value = "70"
evidence = "bench:x"
why = "w"
[[profile.value]]
harness = "claude_code"
key = "claudeMdExcludes"
list = ["**/vendor/CLAUDE.md"]
evidence = "none"
why = "w"
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(c); err != nil {
		t.Fatal(err)
	}
	pr, _ := p.Get("t")
	if sel := pr.Select(c, ClaudeCode, false); len(sel.Use) != 1 || len(sel.Skipped) != 1 {
		t.Errorf("evidence-backed value should be used and the unevidenced one skipped: %+v", sel)
	}
}
