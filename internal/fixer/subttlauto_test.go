package fixer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/store"
)

func withDB(t *testing.T, f *fx) {
	t.Helper()
	db, err := store.Open(f.env.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f.env.DB = db.DB
}

func TestSubTTLUserChoice(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fx)
		want  string // substring; "" = nothing the user set
	}{
		{name: "nothing set", setup: func(*fx) {}},
		{name: "shell variable", setup: func(f *fx) {
			f.env.Getenv = func(k string) string {
				if k == "CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL" {
					return "5m"
				}
				return ""
			}
		}, want: "CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL"},
		{name: "force five minutes in settings env", setup: func(f *fx) {
			f.writeAbs(filepath.Join(f.env.ClaudeDir, "settings.json"), `{"env": {"FORCE_PROMPT_CACHING_5M": "1"}}`)
		}, want: "FORCE_PROMPT_CACHING_5M"},
		{name: "key in user settings", setup: func(f *fx) {
			f.writeAbs(filepath.Join(f.env.ClaudeDir, "settings.json"), `{"subagentPromptCacheTtl": "5m"}`)
		}, want: "subagentPromptCacheTtl"},
		{name: "key in project settings", setup: func(f *fx) {
			f.write(".claude/settings.local.json", `{"subagentPromptCacheTtl": "1h"}`)
		}, want: "subagentPromptCacheTtl"},
		{name: "subagent frontmatter", setup: func(f *fx) {
			f.writeAbs(filepath.Join(f.env.ClaudeDir, "agents", "rev.md"), "---\nname: rev\nexperimental:\n  cacheTtl: 5m\n---\nbody\n")
		}, want: "one of your subagents"},
		{name: "frontmatter word in the body only", setup: func(f *fx) {
			f.writeAbs(filepath.Join(f.env.ClaudeDir, "agents", "rev.md"), "---\nname: rev\n---\ncacheTtl: is mentioned here\n")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFx(t)
			withDB(t, f)
			c.setup(f)
			got := SubTTLUserChoice(context.Background(), f.env, "")
			if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestSetSubagentTTLIsAutopilotsOwnAndReverts(t *testing.T) {
	f := newFx(t)
	withDB(t, f)
	settings := filepath.Join(f.env.ClaudeDir, "settings.json")
	orig := "{\n  \"theme\": \"dark\"\n}\n"
	f.writeAbs(settings, orig)
	ctx := context.Background()

	id, err := SetSubagentTTL(ctx, f.env, "1h", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(settings)
	if !strings.Contains(string(b), `"subagentPromptCacheTtl": "1h"`) {
		t.Fatalf("setting not written:\n%s", b)
	}
	if why := SubTTLUserChoice(ctx, f.env, id); why != "" {
		t.Fatalf("Autopilot's own value was taken for the user's: %s", why)
	}
	if why := SubTTLUserChoice(ctx, f.env, ""); why == "" {
		t.Fatal("without the id the value must count as the user's")
	}
	if on, err := FixInPlace(ctx, f.env, id); err != nil || !on {
		t.Fatal("fix not recorded")
	}
	f.env.Now = f.env.Now.Add(15 * 24 * time.Hour)
	if err := RevertSubagentTTL(ctx, f.env, id, "1h"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(settings); string(b) != orig {
		t.Fatalf("undo not byte-exact:\n%s", b)
	}
	if on, _ := FixInPlace(ctx, f.env, id); on {
		t.Fatal("fix still recorded as in place")
	}
}

func TestRevertSubagentTTLAfterTheFileWasEdited(t *testing.T) {
	f := newFx(t)
	withDB(t, f)
	settings := filepath.Join(f.env.ClaudeDir, "settings.json")
	f.writeAbs(settings, "{\n  \"theme\": \"dark\"\n}\n")
	ctx := context.Background()
	id, err := SetSubagentTTL(ctx, f.env, "1h", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(settings)
	edited := strings.Replace(string(b), `"theme": "dark"`, `"theme": "light"`, 1)
	f.writeAbs(settings, edited)
	if err := RevertSubagentTTL(ctx, f.env, id, "1h"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(settings)
	if strings.Contains(string(b), SubTTLKey) || !strings.Contains(string(b), `"light"`) {
		t.Fatalf("want only the key removed and the user's edit kept:\n%s", b)
	}
	if on, _ := FixInPlace(ctx, f.env, id); on {
		t.Fatal("fix still recorded as in place")
	}
}

func TestOtherSessionActiveIgnoresTheEndingSession(t *testing.T) {
	f := newFx(t)
	proj := filepath.Join(f.env.ClaudeDir, "projects", "p")
	mine := filepath.Join(proj, "sess-mine.jsonl")
	f.writeAbs(mine, "{}\n")
	f.writeAbs(filepath.Join(proj, "sess-mine", "subagents", "agent-1.jsonl"), "{}\n")
	f.env.Now = time.Now()
	ctx := context.Background()
	if on, why := OtherSessionActive(ctx, f.env, "sess-mine"); on {
		t.Fatalf("the ending session counted as another: %s", why)
	}
	f.writeAbs(filepath.Join(proj, "sess-other.jsonl"), "{}\n")
	if on, _ := OtherSessionActive(ctx, f.env, "sess-mine"); !on {
		t.Fatal("a fresh transcript of another session must count")
	}
}

func (f *fx) writeAbs(p, body string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}
