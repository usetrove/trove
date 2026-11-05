package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAdapterEventsRoundTripAndAreMetadataOnly(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	in, out, cost := int64(1000), int64(50), 0.25
	ts := time.Now()
	evs := []AdapterEvent{
		{TS: ts, SessionKey: "h-1", Harness: "opencode", Login: "api_key", Provider: "anthropic", Model: "m", Kind: "rewritten", Optimizer: "noise_normalizer", State: "on", Bytes: 400, Tokens: 100, OverheadTokens: 3},
		{TS: ts, SessionKey: "h-1", Harness: "opencode", Login: "api_key", Kind: "would_rewrite", Optimizer: "noise_normalizer", State: "shadow", Tokens: 50},
		{TS: ts, SessionKey: "h-1", Harness: "opencode", Login: "api_key", Kind: "usage", InTokens: &in, OutTokens: &out, CostUSD: &cost},
		{TS: ts, SessionKey: "h-2", Harness: "opencode", Login: "copilot", Kind: "usage", InTokens: &in},
	}
	if err := db.AdapterEvents().Append(ctx, evs); err != nil {
		t.Fatal(err)
	}
	dec, err := db.AdapterEvents().Decisions(ctx, ts.Add(-time.Minute))
	if err != nil || len(dec) != 2 {
		t.Fatalf("decisions: %v %+v", err, dec)
	}
	if d := dec[0]; d.Optimizer != "noise_normalizer" || d.State != "on" || d.Events != 1 || d.Changed != 1 || d.Tokens != 100 || d.OverheadTokens != 3 {
		t.Errorf("%+v", d)
	}
	use, err := db.AdapterEvents().Usage(ctx, ts.Add(-time.Minute))
	if err != nil || len(use) != 2 {
		t.Fatalf("usage: %v %+v", err, use)
	}
	if u := use[0]; u.Login != "api_key" || u.In != 1000 || u.Out != 50 || u.CostKnown != 1 || u.CostUSD != 0.25 || u.Sessions != 1 {
		t.Errorf("%+v", u)
	}
	if u := use[1]; u.Login != "copilot" || u.CostKnown != 0 {
		t.Errorf("a call that reported no cost must not count as one that did: %+v", u)
	}
	if rows, _ := db.AdapterEvents().Decisions(ctx, ts.Add(time.Hour)); len(rows) != 0 {
		t.Error("rows from before the period were included")
	}
}

func TestAdapterEventsRejectValuesTheSchemaDoesNotAllow(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for name, e := range map[string]AdapterEvent{
		"unknown login": {TS: time.Now(), SessionKey: "h", Harness: "opencode", Login: "chatgpt", Kind: "usage"},
		"unknown kind":  {TS: time.Now(), SessionKey: "h", Harness: "opencode", Login: "api_key", Kind: "prompt"},
	} {
		if err := db.AdapterEvents().Append(context.Background(), []AdapterEvent{e}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The columns hold numbers and enum labels only. This checks the schema itself:
// no column that could carry prompt text, a path, a command or a credential.
func TestAdapterEventsSchemaHasNoFreeTextColumn(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), `SELECT name FROM pragma_table_info('adapter_events')`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var cols []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, n)
	}
	b, _ := json.Marshal(cols)
	for _, banned := range []string{"text", "prompt", "content", "path", "command", "output", "key_", "token", "secret", "auth", "header", "body"} {
		for _, c := range cols {
			if c == "session_key" || strings.HasSuffix(c, "_tokens") || c == "tokens" || c == "overhead_tokens" {
				continue
			}
			if strings.Contains(c, banned) {
				t.Errorf("column %q looks like it could hold %s: %s", c, banned, b)
			}
		}
	}
}
