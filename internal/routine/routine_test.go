package routine

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		prompt     string
		routine    bool
		correction bool
		reason     string
	}{
		// Plain confirmations.
		{"yes", true, false, ReasonRoutine},
		{"Yes", true, false, ReasonRoutine},
		{"YES!", true, false, ReasonRoutine},
		{"  yes  ", true, false, ReasonRoutine},
		{"y", true, false, ReasonRoutine},
		{"yep", true, false, ReasonRoutine},
		{"yeah.", true, false, ReasonRoutine},
		{"ok", true, false, ReasonRoutine},
		{"OK.", true, false, ReasonRoutine},
		{"okay", true, false, ReasonRoutine},
		{"k", true, false, ReasonRoutine},
		{"sure", true, false, ReasonRoutine},
		{"sounds good", true, false, ReasonRoutine},
		{"Sounds good!", true, false, ReasonRoutine},
		{"looks good", true, false, ReasonRoutine},
		{"LGTM", true, false, ReasonRoutine},
		{"perfect", true, false, ReasonRoutine},
		{"great, thanks", true, false, ReasonRoutine},
		{"go ahead", true, false, ReasonRoutine},
		{"Go ahead.", true, false, ReasonRoutine},
		{"go for it", true, false, ReasonRoutine},
		{"do it", true, false, ReasonRoutine},
		{"let's do it", true, false, ReasonRoutine},
		{"let’s do it", true, false, ReasonRoutine},
		{"ship it", true, false, ReasonRoutine},
		{"proceed", true, false, ReasonRoutine},
		{"please proceed", true, false, ReasonRoutine},
		{"approved", true, false, ReasonRoutine},
		{"yes please", true, false, ReasonRoutine},
		{"Yes, please.", true, false, ReasonRoutine},
		{"yes go ahead", true, false, ReasonRoutine},
		{"yes, go ahead", true, false, ReasonRoutine},
		{"ok sure", true, false, ReasonRoutine},
		{"ok. continue", true, false, ReasonRoutine},
		{"sure thing", true, false, ReasonRoutine},
		{"makes sense, go ahead", true, false, ReasonRoutine},
		{"that works", true, false, ReasonRoutine},
		{"thank you, continue", true, false, ReasonRoutine},
		{"thanks!", false, false, ReasonOnlyPolite},
		{"please", false, false, ReasonOnlyPolite},

		// Continuations.
		{"continue", true, false, ReasonRoutine},
		{"Continue.", true, false, ReasonRoutine},
		{"continue please", true, false, ReasonRoutine},
		{"please continue", true, false, ReasonRoutine},
		{"keep going", true, false, ReasonRoutine},
		{"go on", true, false, ReasonRoutine},
		{"carry on", true, false, ReasonRoutine},
		{"next", true, false, ReasonRoutine},
		{"next step", true, false, ReasonRoutine},
		{"ok, next", true, false, ReasonRoutine},
		{"resume", true, false, ReasonRoutine},
		{"proceed with the plan", true, false, ReasonRoutine},
		{"yes, continue with the plan", true, false, ReasonRoutine},
		{"move on", true, false, ReasonRoutine},

		// Running things.
		{"run the tests", true, false, ReasonRoutine},
		{"Run the tests.", true, false, ReasonRoutine},
		{"run tests", true, false, ReasonRoutine},
		{"run the test suite", true, false, ReasonRoutine},
		{"rerun the tests", true, false, ReasonRoutine},
		{"re-run the tests", true, false, ReasonRoutine},
		{"run the tests again", true, false, ReasonRoutine},
		{"ok run the tests", true, false, ReasonRoutine},
		{"yes, run the tests", true, false, ReasonRoutine},
		{"build it", true, false, ReasonRoutine},
		{"run the build", true, false, ReasonRoutine},
		{"lint", true, false, ReasonRoutine},
		{"run the linter", true, false, ReasonRoutine},
		{"format it", true, false, ReasonRoutine},
		{"typecheck", true, false, ReasonRoutine},
		{"run it", true, false, ReasonRoutine},

		// Version control.
		{"commit", true, false, ReasonRoutine},
		{"Commit.", true, false, ReasonRoutine},
		{"commit it", true, false, ReasonRoutine},
		{"commit the changes", true, false, ReasonRoutine},
		{"commit and push", true, false, ReasonRoutine},
		{"yes, commit and push", true, false, ReasonRoutine},
		{"ok commit", true, false, ReasonRoutine},
		{"looks good, commit it", true, false, ReasonRoutine},
		{"push", true, false, ReasonRoutine},
		{"push it", true, false, ReasonRoutine},
		{"open a pr", true, false, ReasonRoutine},
		{"run the tests, then commit", false, false, ReasonUnlisted},

		// Tricky: a routine opening with real content after it.
		{"yes but fix the race first", false, false, ReasonUnlisted},
		{"Yes, but fix the race first.", false, false, ReasonUnlisted},
		{"yes and also update the readme", false, false, ReasonUnlisted},
		{"ok but use a mutex", false, false, ReasonUnlisted},
		{"continue, but skip the migration", false, false, ReasonUnlisted},
		{"commit, but not the lockfile", false, false, ReasonUnlisted},
		{"run the tests and fix what fails", false, false, ReasonUnlisted},
		{"run the tests for the parser", false, false, ReasonUnlisted},
		{"run the integration tests", false, false, ReasonUnlisted},
		{"go ahead with option b", false, false, ReasonUnlisted},
		{"yes, the second one", false, false, ReasonUnlisted},
		{"commit with a better message", false, false, ReasonUnlisted},
		{"push to staging", false, false, ReasonUnlisted},
		{"sure, rename it to Fetcher", false, false, ReasonUnlisted},
		{"go ahead and refactor the cache", false, false, ReasonUnlisted},
		{"continue until all tests pass", false, false, ReasonUnlisted},
		{"keep going but be careful with the schema", false, false, ReasonUnlisted},
		{"yes if the benchmarks agree", false, false, ReasonUnlisted},
		{"ok, think carefully about this", false, false, ReasonUnlisted},
		{"proceed carefully", false, false, ReasonUnlisted},
		{"yes but think it through", false, false, ReasonUnlisted},
		{"fix it", false, false, ReasonUnlisted},
		{"fix the race", false, false, ReasonUnlisted},
		{"implement it", false, false, ReasonUnlisted},
		{"refactor", false, false, ReasonUnlisted},
		{"explain", false, false, ReasonUnlisted},
		{"explain why", false, false, ReasonUnlisted},
		{"debug this", false, false, ReasonUnlisted},
		{"review the diff", false, false, ReasonUnlisted},
		{"write tests", false, false, ReasonUnlisted},
		{"hello", false, false, ReasonUnlisted},
		{"and", false, false, ReasonUnlisted},
		{"yes yes and", false, false, ReasonUnlisted},

		// Questions.
		{"continue?", false, false, ReasonQuestion},
		{"ok?", false, false, ReasonQuestion},
		{"yes, why not?", false, false, ReasonQuestion},
		{"should we commit?", false, false, ReasonQuestion},
		{"why?", false, false, ReasonQuestion}, // only "why did you ..." opens a correction
		{"what about the race?", false, false, ReasonQuestion},

		// Symbols, numbers, paths, code, markup.
		{"yes 2", false, false, ReasonSymbols},
		{"option 2", false, false, ReasonSymbols},
		{"1", false, false, ReasonSymbols},
		{"commit -m wip", false, false, ReasonUnlisted},
		{"run `go test ./...`", false, false, ReasonSymbols},
		{"yes: internal/hooks", false, false, ReasonSymbols},
		{"commit src/main.go", false, false, ReasonSymbols},
		{"yes (but carefully)", false, false, ReasonSymbols},
		{"go ahead @alice", false, false, ReasonSymbols},
		{"ok 👍", false, false, ReasonSymbols},
		{"👍", false, false, ReasonSymbols},
		{"sí", false, false, ReasonSymbols},
		{"x = 1", false, false, ReasonSymbols},

		// Slash commands and shell escapes.
		{"/commit", false, false, ReasonSlash},
		{"/compact", false, false, ReasonSlash},
		{"!git status", false, false, ReasonSlash},
		{"# remember this", false, false, ReasonSlash},

		// Shape limits.
		{"", false, false, ReasonEmpty},
		{"   ", false, false, ReasonEmpty},
		{"...", false, false, ReasonEmpty},
		{"!!!", false, false, ReasonEmpty},
		{"yes\ncontinue", false, false, ReasonMultiline},
		{"yes\r\n", true, false, ReasonRoutine}, // a trailing newline is trimmed
		{"continue\n\nbut also fix the test", false, false, ReasonMultiline},
		{"yes yes yes yes yes yes yes yes yes", false, false, ReasonTooLong},
		{strings.Repeat("ok ", 30), false, false, ReasonTooLong},
		{strings.Repeat("y", 500), false, false, ReasonTooLong},
		{"\xff\xfe", false, false, ReasonNotText},

		// Corrections: never routine, and flagged for the re-prompt count.
		{"no", false, true, ReasonCorrection},
		{"No.", false, true, ReasonCorrection},
		{"nope", false, true, ReasonCorrection},
		{"no, continue", false, true, ReasonCorrection},
		{"no, that's wrong", false, true, ReasonCorrection},
		{"that's not right", false, true, ReasonCorrection},
		{"That’s not what I asked", false, true, ReasonCorrection},
		{"wrong file", false, true, ReasonCorrection},
		{"try again", false, true, ReasonCorrection},
		{"Try again.", false, true, ReasonCorrection},
		{"again", false, true, ReasonCorrection},
		{"it still fails", false, true, ReasonCorrection},
		{"still failing", false, true, ReasonCorrection},
		{"the build still fails", false, true, ReasonCorrection},
		{"that didn't work", false, true, ReasonCorrection},
		{"the fix didn't work", false, true, ReasonCorrection},
		{"undo that", false, true, ReasonCorrection},
		{"revert it", false, true, ReasonCorrection},
		{"you forgot the tests", false, true, ReasonCorrection},
		{"I said run the tests", false, true, ReasonCorrection},
		{"why did you delete that?", false, true, ReasonCorrection},
		{"wait", false, true, ReasonCorrection},
		{"stop", false, true, ReasonCorrection},
		{"actually, use a map", false, true, ReasonCorrection},
		{"don't commit yet", false, true, ReasonCorrection},
		{"not what I meant", false, true, ReasonCorrection},
		{"you broke the build", false, true, ReasonCorrection},
		{"no\nplease redo the parser", false, true, ReasonMultiline},

		// Words that contain correction words are not corrections.
		{"notes", false, false, ReasonUnlisted},
		{"nothing else", false, false, ReasonUnlisted},
		{"knowing that, continue", false, false, ReasonUnlisted},
		{"stopwatch", false, false, ReasonUnlisted},
		{"waiting room", false, false, ReasonUnlisted},
	}
	for _, c := range cases {
		got := Classify(c.prompt)
		if got.Routine != c.routine || got.Correction != c.correction || got.Reason != c.reason {
			t.Errorf("Classify(%q) = %+v, want routine=%v correction=%v reason=%s", c.prompt, got, c.routine, c.correction, c.reason)
		}
	}
}

// TestDeterministic: the same prompt always gets the same label, so a resumed session is steered the same way.
func TestDeterministic(t *testing.T) {
	for _, p := range []string{"yes", "yes but fix the race first", "run the tests", "no", "", "commit and push"} {
		first := Classify(p)
		for i := 0; i < 100; i++ {
			if Classify(p) != first {
				t.Fatalf("Classify(%q) changed between calls", p)
			}
		}
	}
}

// TestEveryPhraseIsRoutine keeps the list honest: every listed phrase, alone, is routine, and none is a correction.
func TestEveryPhraseIsRoutine(t *testing.T) {
	for p := range phrases {
		if got := Classify(p); !got.Routine {
			t.Errorf("listed phrase %q is not routine: %+v", p, got)
		}
		if len(p) > MaxBytes || len(strings.Fields(p)) > MaxWords {
			t.Errorf("listed phrase %q is longer than a routine prompt may be", p)
		}
	}
}

// TestNoPhraseStartsACorrection: a listed phrase must not also open a correction, or it could never be routine.
func TestNoPhraseStartsACorrection(t *testing.T) {
	for p := range phrases {
		if isCorrection(p) {
			t.Errorf("listed phrase %q reads as a correction", p)
		}
	}
}

// TestReasonsAreFixedWords: a reason never carries prompt text.
func TestReasonsAreFixedWords(t *testing.T) {
	known := map[string]bool{ReasonRoutine: true, ReasonEmpty: true, ReasonTooLong: true, ReasonMultiline: true,
		ReasonQuestion: true, ReasonSymbols: true, ReasonSlash: true, ReasonCorrection: true, ReasonUnlisted: true,
		ReasonNotText: true, ReasonOnlyPolite: true}
	for _, p := range []string{"SECRET-TOKEN-123", "yes", "fix internal/x.go", "no way", "\xff", "a\nb", "?"} {
		if r := Classify(p).Reason; !known[r] {
			t.Errorf("Classify(%q).Reason = %q, not a fixed reason code", p, r)
		}
	}
}

func FuzzClassify(f *testing.F) {
	for _, s := range []string{"yes", "yes but fix the race first", "no", "run the tests", "\xff", "a\nb", "commit and push"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		l := Classify(s)
		if l.Routine && l.Correction {
			t.Fatalf("%q is both routine and a correction", s)
		}
		if l.Routine && (len(strings.TrimSpace(s)) > MaxBytes || strings.ContainsAny(strings.TrimSpace(s), "\n\r?")) {
			t.Fatalf("%q is routine but breaks a shape limit", s)
		}
		if l.Reason == "" {
			t.Fatalf("%q has no reason", s)
		}
	})
}
