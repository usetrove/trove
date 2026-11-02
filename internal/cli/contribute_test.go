package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func contributeHarness(t *testing.T) (h *harness, dir string, hits *int32, last *string) {
	t.Helper()
	hits, last = new(int32), new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		b, _ := io.ReadAll(r.Body)
		*last = string(b)
	}))
	t.Cleanup(srv.Close)
	h = newHarness(t, nil)
	dir = h.app.Getenv("TROVE_DATA_DIR")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[contribute]\nendpoint = \"" + srv.URL + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return h, dir, hits, last
}

func (h *harness) reset() { h.out.Reset(); h.err.Reset() }

func TestContributeSendsNothingUntilEnabled(t *testing.T) {
	h, _, hits, _ := contributeHarness(t)
	for _, args := range [][]string{{"report", "--no-history"}, {"contribute", "--no-history"}, {"contribute", "status"}} {
		h.reset()
		if code := h.run(args...); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, h.err)
		}
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("server was contacted %d times before consent", n)
	}
}

func TestContributeEnableReportDisable(t *testing.T) {
	h, dir, hits, last := contributeHarness(t)

	// Without a yes the preview is shown and nothing is turned on.
	if code := h.run("contribute", "enable", "--no-history"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), "exactly this text") || !strings.Contains(h.out.String(), "Nothing changed") {
		t.Errorf("enable without consent:\n%s", h.out)
	}
	if _, err := os.Stat(filepath.Join(dir, "contribute.json")); err == nil {
		t.Fatal("consent record written without a yes")
	}

	h.reset()
	if code := h.run("contribute", "enable", "--yes", "--no-history"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if atomic.LoadInt32(hits) != 0 {
		t.Fatal("enable itself must not upload")
	}

	// A report with an explicit period is never shared.
	h.reset()
	h.run("report", "--today", "--no-history")
	if atomic.LoadInt32(hits) != 0 {
		t.Fatal("a --today report was shared")
	}

	// The default report shares once, then waits a week.
	h.reset()
	h.run("report", "--no-history")
	if atomic.LoadInt32(hits) != 1 {
		t.Fatalf("hits = %d, want 1", atomic.LoadInt32(hits))
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(*last), &sent); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, *last)
	}
	if id, _ := sent["install_id"].(string); len(id) != 32 {
		t.Errorf("install_id = %v", sent["install_id"])
	}
	h.reset()
	h.run("report", "--no-history")
	if atomic.LoadInt32(hits) != 1 {
		t.Errorf("shared twice within a week: %d hits", atomic.LoadInt32(hits))
	}

	// The preview shows what was sent, byte for byte.
	h.reset()
	h.run("contribute", "--no-history")
	if !strings.Contains(h.out.String(), strings.TrimSpace(*last)) {
		t.Errorf("preview differs from the upload:\n%s\nvs\n%s", h.out, *last)
	}

	// Disable stops it, even after a reset makes an upload due again.
	h.reset()
	if code := h.run("contribute", "disable"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	h.reset()
	h.run("contribute", "reset")
	h.run("report", "--no-history")
	if atomic.LoadInt32(hits) != 1 {
		t.Errorf("uploaded after disable: %d hits", atomic.LoadInt32(hits))
	}
}

func TestContributeEnableNeedsAnEndpoint(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("contribute", "enable", "--yes", "--no-history"); code == 0 {
		t.Fatal("enable without an endpoint should fail")
	}
	if !strings.Contains(h.err.String(), "endpoint") {
		t.Errorf("error should say what to do: %s", h.err)
	}
}
