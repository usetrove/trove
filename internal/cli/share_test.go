package cli

import (
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/store"
)

func seedShareData(t *testing.T, h *harness) {
	t.Helper()
	db, err := store.Open(h.app.Getenv("TROVE_DATA_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	findings := `[{"detector":"duplicate_tool_result","tokens":4000,"usd":0.3,"confidence":"high","label":"estimated","detail":{"path":"/home/me/acme-secret-repo/main.go"}}]`
	for i := 0; i < 25; i++ {
		if _, err := db.Exec(`INSERT INTO requests(ts, session_key, agent, provider, model, status_code, input_uncached_tokens, output_tokens, cost_usd, findings)
			VALUES(?, ?, 'claude_code', 'anthropic', 'acme-private-model', 200, 1000, 100, 1.5, ?)`,
			time.Now().Add(-time.Duration(i)*time.Minute).UnixMilli(), "s"+string(rune('a'+i)), findings); err != nil {
			t.Fatal(err)
		}
	}
}

func TestShareNothingYet(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("share", "--yes"); code != 0 || !strings.Contains(h.out.String(), "Nothing to share yet") {
		t.Errorf("exit %d: %q %q", code, h.out, h.err)
	}
}

func TestShareShowsPreviewAndAsksBeforeSaving(t *testing.T) {
	h := newHarness(t, nil)
	seedShareData(t, h)
	out := filepath.Join(t.TempDir(), "cards")

	// No terminal and no --yes: preview only, nothing written.
	if code := h.run("share", "--out", out); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	for _, want := range []string{"Share card preview", "Spend", "(estimated)", "Repeated tool results", "no repository, file, prompt, agent or model names", "Nothing saved"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("preview lacks %q:\n%s", want, h.out)
		}
	}
	if _, err := os.Stat(out + ".png"); err == nil {
		t.Fatal("saved without consent")
	}

	// Answering n saves nothing; y saves both files.
	h.out.Reset()
	h.app.In = strings.NewReader("n\n")
	h.run("share", "--out", out)
	if exists(out + ".svg") {
		t.Fatal("answering n must not save")
	}
	h.out.Reset()
	h.app.In = strings.NewReader("y\n")
	if code := h.run("share", "--out", out, "--theme", "light"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	f, err := os.Open(out + ".png")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := png.Decode(f); err != nil {
		t.Errorf("saved PNG is not valid: %v", err)
	}
	svg, _ := os.ReadFile(out + ".svg")
	for _, secret := range []string{"acme-secret-repo", "acme-private-model", "main.go", "claude_code"} {
		if strings.Contains(string(svg), secret) {
			t.Errorf("the saved card contains %q", secret)
		}
	}
}

func TestShareJSONSavesOnlyWithYes(t *testing.T) {
	h := newHarness(t, nil)
	seedShareData(t, h)
	dir := t.TempDir()

	if code := h.run("share", "--json", "--out", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var v shareOutput
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil || v.Saved || v.Card == nil || v.Card.Label != "estimated" {
		t.Errorf("json: %q (%v)", h.out, err)
	}
	if exists(filepath.Join(dir, "trove-share.svg")) {
		t.Error("--json without --yes must not save")
	}

	h.out.Reset()
	if code := h.run("share", "--json", "--yes", "--out", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	v = shareOutput{}
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil || !v.Saved || !exists(v.PNG) || !exists(v.SVG) {
		t.Errorf("json: %q (%v)", h.out, err)
	}
	if code := h.run("share", "--theme", "blue"); code != 1 {
		t.Errorf("a bad theme should fail, exit %d", code)
	}
}
