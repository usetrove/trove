package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/store"
)

func TestReceiptsCatchUpAndList(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("receipts"); code != 0 || !strings.Contains(h.out.String(), "No finished sessions yet") {
		t.Fatalf("empty: exit %d: %q %q", code, h.out, h.err)
	}

	db, err := store.Open(h.app.Getenv("TROVE_DATA_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	ended := time.Now().Add(-2 * time.Hour)
	for i := 0; i < 3; i++ {
		ts := ended.Add(-time.Duration(i) * time.Minute).UnixMilli()
		if _, err := db.Exec(`INSERT INTO requests(ts, session_key, agent, model, status_code, input_uncached_tokens, cache_read_tokens, cost_usd)
			VALUES(?, 'c-0123456789abcdef01234567', 'claude_code', 'claude-a', 200, 100, 300, 0.25)`, ts); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO sessions(session_key, agent, started_at, ended_at, request_count) VALUES('c-0123456789abcdef01234567','claude_code',?,?,3)`,
		ended.Add(-2*time.Minute).UnixMilli(), ended.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	h.out.Reset()
	if code := h.run("receipts"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	for _, want := range []string{"claude_code", "claude-a", "3 calls", "75% cache hits (measured)", "(estimated)"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("receipts lack %q:\n%s", want, h.out)
		}
	}

	h.out.Reset()
	if code := h.run("receipts", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var v struct {
		Receipts []struct {
			Calls int `json:"calls"`
			Cost  struct {
				Value float64 `json:"value"`
				Label string  `json:"label"`
			} `json:"cost_usd"`
		} `json:"receipts"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil || len(v.Receipts) != 1 || v.Receipts[0].Calls != 3 ||
		v.Receipts[0].Cost.Value != 0.75 || v.Receipts[0].Cost.Label != "estimated" {
		t.Errorf("json: %q (%v)", h.out, err)
	}

	if code := h.run("receipts", "--limit", "0"); code != 1 {
		t.Errorf("--limit 0 should fail, exit %d", code)
	}
}
