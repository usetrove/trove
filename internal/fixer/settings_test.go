package fixer

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/setup"
)

func versions(h string) string {
	return map[string]string{"claude_code": "2.1.314 (Claude Code)", "codex": "codex-cli 0.134.0"}[h]
}

func settingsFixes(t *testing.T, e *Env) []*Fix {
	t.Helper()
	all, errs := Detect(context.Background(), e)
	if len(errs) > 0 {
		t.Fatalf("detect errors: %v", errs)
	}
	var out []*Fix
	for _, x := range all {
		if x.Kind == SettingsProfile {
			out = append(out, x)
		}
	}
	return out
}

func TestSettingsFixOnlyWhenRequested(t *testing.T) {
	f := newFx(t)
	f.env.SettingsVersion = versions
	if got := settingsFixes(t, f.env); len(got) != 0 {
		t.Fatalf("plain detection must not plan settings: %+v", got)
	}
}

func TestSettingsCodexProfileApplyAndUndo(t *testing.T) {
	f := newFx(t)
	f.env.SettingsVersion = versions
	f.env.Settings = &SettingsRequest{Task: "quick-fix", AllowUnproven: true}
	fixes := settingsFixes(t, f.env)
	var x *Fix
	for _, c := range fixes {
		if c.Agent == "codex" {
			x = c
		}
	}
	if x == nil || !x.Applicable() || !x.Suggestion {
		t.Fatalf("want an applicable codex suggestion, got %+v", fixes)
	}
	want := filepath.Join(f.env.CodexDir, "trove-quick-fix.config.toml")
	if x.Path != want {
		t.Fatalf("path = %s, want %s", x.Path, want)
	}
	for _, s := range []string{`+model_reasoning_summary = "none"`, `+model_verbosity = "low"`} {
		if !strings.Contains(x.Diff, s) {
			t.Errorf("diff misses %q:\n%s", s, x.Diff)
		}
	}
	if x.Label != "estimated" || x.TokensPerSession != 0 || x.USDPerSession != nil {
		t.Errorf("no saving may be claimed before a measurement: %+v", x)
	}
	if !strings.Contains(strings.Join(x.Notes, "\n"), "codex --profile trove-quick-fix") {
		t.Errorf("notes should say how to use the profile: %v", x.Notes)
	}
	// --all must never pick it up.
	if f.applyAllCount(SettingsProfile) != 0 {
		t.Fatal("settings fixes are suggestions and must not be part of --all")
	}

	res, err := Apply(context.Background(), f.env, []string{x.ID})
	if err != nil || len(res) != 1 || res[0].Status != "applied" {
		t.Fatalf("apply: %+v %v", res, err)
	}
	body := f.read(want)
	if !strings.Contains(body, `model_reasoning_summary = "none"`) || !strings.Contains(body, "trove undo") {
		t.Errorf("profile file:\n%s", body)
	}
	// The user's own config.toml is not touched.
	if _, err := os.Stat(filepath.Join(f.env.CodexDir, "config.toml")); err == nil {
		t.Error("the user's config.toml must not be created or changed")
	}
	// Applying again finds nothing to do.
	if again := settingsFixes(t, f.env); len(again) != 0 {
		for _, a := range again {
			if a.Agent == "codex" {
				t.Errorf("already applied, but planned again: %+v", a)
			}
		}
	}
	// Undo removes the file Trove created.
	if _, err := setup.Undo(f.env.DataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(want); err == nil {
		t.Error("undo must remove a profile file Trove created")
	}
}

func (f *fx) applyAllCount(kind string) int {
	all, _ := Detect(context.Background(), f.env)
	n := 0
	for _, x := range all {
		if x.Kind == kind && x.Applicable() && !x.Suggestion {
			n++
		}
	}
	return n
}

func TestSettingsClaudeKeepsUserFileAndUndoIsByteExact(t *testing.T) {
	f := newFx(t)
	f.env.SettingsVersion = versions
	f.env.Settings = &SettingsRequest{Task: "exploration", AllowUnproven: true}
	orig := "{\r\n    \"permissions\": {\"allow\": [\"Bash(go test:*)\"]},\r\n    \"model\": \"sonnet\"\r\n}\r\n"
	p := f.write(".claude/settings.json", orig)
	var x *Fix
	for _, c := range settingsFixes(t, f.env) {
		if c.Agent == "claude_code" {
			x = c
		}
	}
	if x == nil || !x.Applicable() {
		t.Fatalf("want an applicable claude fix, got %+v", x)
	}
	if _, err := Apply(context.Background(), f.env, []string{x.ID}); err != nil {
		t.Fatal(err)
	}
	got := f.read(p)
	if !strings.Contains(got, `"outputStyle": "Concise"`) || !strings.Contains(got, `"permissions": {"allow": [`) || !strings.Contains(got, "\r\n") || strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Errorf("user content and CRLF must survive:\n%q", got)
	}
	if _, err := setup.Undo(f.env.DataDir); err != nil {
		t.Fatal(err)
	}
	if f.read(p) != orig {
		t.Errorf("undo must restore the file byte for byte:\n%q", f.read(p))
	}
}

func TestSettingsNeverOverridesManaged(t *testing.T) {
	f := newFx(t)
	f.env.SettingsVersion = versions
	f.env.Settings = &SettingsRequest{Task: "exploration", AllowUnproven: true}
	f.env.ReadFile = func(p string) ([]byte, error) {
		switch filepath.Base(p) {
		case "managed-settings.json":
			return []byte(`{"outputStyle": "Learning"}`), nil
		case "managed_config.toml":
			return []byte("model_reasoning_summary = \"auto\"\n"), nil
		}
		return os.ReadFile(p)
	}
	for _, x := range settingsFixes(t, f.env) {
		if x.Applicable() || x.Blocked == "" {
			t.Errorf("%s: every value is set by managed policy, so the fix must be blocked: %+v", x.Agent, x)
		}
		if !strings.Contains(x.Blocked, "cannot be overridden") {
			t.Errorf("%s: the reason must say why: %s", x.Agent, x.Blocked)
		}
		res, _ := Apply(context.Background(), f.env, []string{x.ID})
		if len(res) != 1 || res[0].Status != "refused" {
			t.Errorf("%s: apply must refuse: %+v", x.Agent, res)
		}
		if _, err := os.Stat(x.Path); err == nil {
			t.Errorf("%s: nothing may be written", x.Agent)
		}
	}
}

func TestSettingsUnknownVersionPlansNothing(t *testing.T) {
	f := newFx(t)
	f.env.SettingsVersion = func(string) string { return "" }
	f.env.Settings = &SettingsRequest{Task: "quick-fix", AllowUnproven: true}
	for _, x := range settingsFixes(t, f.env) {
		if x.Applicable() {
			t.Errorf("%s: an unknown harness version must not produce a write: %+v", x.Agent, x)
		}
	}
}

func TestSettingsQueuedDuringSessionThenApplied(t *testing.T) {
	f := newFx(t)
	f.env.SettingsVersion = versions
	f.env.Settings = &SettingsRequest{Task: "quick-fix"}
	f.sessions(6, []string{"Read"}, now.Add(-5*time.Minute))
	var x *Fix
	for _, c := range settingsFixes(t, f.env) {
		if c.Agent == "codex" {
			x = c
		}
	}
	if x == nil {
		t.Fatal("expected a codex fix")
	}
	res, err := Apply(context.Background(), f.env, []string{x.ID})
	if err != nil || len(res) != 1 || res[0].Status != "queued" {
		t.Fatalf("want queued, got %+v %v", res, err)
	}
	if _, err := os.Stat(x.Path); err == nil {
		t.Fatal("nothing may be written during an active session")
	}
	q, _ := LoadQueue(f.env.DataDir)
	if len(q) != 1 || q[0].Settings == nil || q[0].Settings.Task != "quick-fix" {
		t.Fatalf("the queue must remember the request: %+v", q)
	}
	// Later, with a fresh Env that has no request, the queue replays it.
	later := *f.env
	later.Settings = nil
	later.Now = now.Add(2 * time.Hour)
	res, err = ApplyQueued(context.Background(), &later)
	if err != nil || len(res) != 1 || res[0].Status != "applied" {
		t.Fatalf("idle: %+v %v", res, err)
	}
	if _, err := os.Stat(x.Path); err != nil {
		t.Errorf("the profile should exist now: %v", err)
	}
}

func TestSettingsReadFileErrorsAreNotFatal(t *testing.T) {
	f := newFx(t)
	f.env.SettingsVersion = versions
	f.env.Settings = &SettingsRequest{Task: "quick-fix"}
	f.env.ReadFile = func(string) ([]byte, error) { return nil, &fs.PathError{Op: "open", Err: errors.New("denied")} }
	if _, errs := Detect(context.Background(), f.env); len(errs) > 0 {
		t.Fatalf("unreadable files must not fail detection: %v", errs)
	}
}
