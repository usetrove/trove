// Package credguard holds the test that keeps Trove away from other tools' credential files.
//
// CLAUDE.md rules 2, 3 and 10: Trove never handles subscription or OAuth credentials. For Codex CLI
// that means ~/.codex/auth.json (or $CODEX_HOME/auth.json), which holds a ChatGPT login's access and
// refresh tokens, and the OS keyring entry Codex uses for the same thing. This test fails the build
// if shipped Go code mentions those places anywhere other than the files listed below, each of which
// only refuses to touch them or tells the user about them.
package credguard

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// credentialNames are the places and names a ChatGPT or Codex login lives in. Codex stores it in
// $CODEX_HOME/auth.json or the keyring service "Codex Auth", and accepts access tokens and keys from the
// environment (codex-rs/login, read 2026-11-06). Trove has no reason to read any of them.
var credentialNames = regexp.MustCompile(`(?i)auth\.json|auth\.lock|Codex Auth|CODEX_ACCESS_TOKEN|CODEX_API_KEY|personal_access_token|agent_identity`)

// allowed lists the only shipped files that may contain one of those names, with why. Adding a file
// here is a privacy decision: say what it does with the name, and make sure it is a refusal.
var allowed = map[string]string{
	"internal/fixer/paths.go":              "deny-list: the fixer refuses to read or write these files",
	"internal/mcp/root/sensitive.go":       "deny-list: the MCP file tools refuse to return these files",
	"internal/gateway/auth/auth.go":        "the refusal message names ~/.codex/auth.json so the user knows what is not supported",
	"internal/cli/setup.go":                "the Codex refusal message says Trove will not read auth.json to find out",
	"internal/logs/plan.go":                "comment: says the plan detector never opens it",
	"internal/credguard/credguard_test.go": "this test",
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			t.Fatal("go.mod not found above " + dir)
		}
	}
}

func TestNoShippedCodeTouchesCodexOrChatGPTCredentials(t *testing.T) {
	root := repoRoot(t)
	var scanned int
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist", "bench", "docs", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") && !strings.Contains(p, "credguard") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		scanned++
		b, err := os.ReadFile(p) //nolint:gosec // walking our own repository
		if err != nil {
			return err
		}
		if m := credentialNames.Find(b); m != nil {
			if _, ok := allowed[rel]; !ok {
				t.Errorf("%s mentions %q. Trove must not read Codex or ChatGPT credentials (CLAUDE.md rules 2 and 3). "+
					"If this file only refuses to touch them, add it to allowed with the reason", rel, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("only %d Go files scanned; the walk is broken and proves nothing", scanned)
	}
	for f := range allowed {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
			t.Errorf("allowed lists %s, which does not exist; remove the entry", f)
		}
	}
}

// Codex's session history (rollout files) is the only thing Trove reads under ~/.codex besides the
// config files it edits, and it reads a named subfolder, never the folder itself.
func TestOnlyTheSessionsFolderIsWalkedUnderCodexHome(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "internal", "logs", "cxhistory", "cxhistory.go")) //nolint:gosec // our own repository
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `filepath.Join(h, ".codex", "sessions")`) {
		t.Fatal("cxhistory no longer reads exactly ~/.codex/sessions; check that it still never lists ~/.codex itself")
	}
}
