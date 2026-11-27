package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/report"
)

// The fixtures follow the full JSON schema on https://code.claude.com/docs/en/statusline (verified
// 2026-11-23). all.json is that example with canary strings in the fields Trove must never read, and
// reset times moved to 2100 so the windows are still current when tests render them.

func window(pct float64, resetsAt int64) *report.LimitWindow {
	at := time.Unix(resetsAt, 0).UTC()
	return &report.LimitWindow{UsedPercent: pct, Label: measure.Measured, ResetsAt: &at}
}

func ptrBool(v bool) *bool  { return &v }
func ptrInt(v int64) *int64 { return &v }

func readFixtureInput(t *testing.T, name string) claudeInput {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "statusline", name))
	if err != nil {
		t.Fatal(err)
	}
	return readClaudeInput(&App{In: bytes.NewReader(b)}, report.FormatClaude)
}

func TestReadClaudeInput(t *testing.T) {
	tests := []struct {
		fixture string
		want    claudeInput
	}{
		{"all.json", claudeInput{
			SessionID: "sess-abc",
			FiveHour:  window(23.5, 4102444800), SevenDay: window(41.2, 4103049600),
			CacheWarm: ptrBool(true), CacheTTLSeconds: ptrInt(3600),
			Model: "claude-opus-5-5", Version: "2.1.290",
		}},
		// Before the first response, or on an API key: no meter at all.
		{"none.json", claudeInput{SessionID: "sess-abc", Model: "claude-opus-5-5", Version: "2.1.290"}},
		{"no_five_hour.json", claudeInput{
			SessionID: "sess-abc", SevenDay: window(41.2, 4103049600),
			CacheWarm: ptrBool(false), CacheTTLSeconds: ptrInt(300),
			Model: "claude-opus-5-5", Version: "2.1.290",
		}},
		{"no_seven_day.json", claudeInput{
			SessionID: "sess-abc", FiveHour: window(99.6, 4102444800),
			Model: "claude-opus-5-5", Version: "2.1.290",
		}},
		{"no_rate_limits_cache_only.json", claudeInput{
			SessionID: "sess-abc", CacheWarm: ptrBool(true), CacheTTLSeconds: ptrInt(300),
			Model: "claude-sonnet-5-5[1m]", Version: "2.1.290",
		}},
		// Out of range, mistyped or free text: each value is dropped on its own; the session id survives.
		{"invalid.json", claudeInput{SessionID: "sess-abc"}},
		{"garbage.txt", claudeInput{}},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			got := readFixtureInput(t, tc.fixture)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("\n got %+v\nwant %+v", describe(got), describe(tc.want))
			}
		})
	}
}

// describe makes the pointers readable in a failure message.
func describe(in claudeInput) string {
	s := fmt.Sprintf("{session=%q model=%q version=%q", in.SessionID, in.Model, in.Version)
	for _, w := range []struct {
		name string
		w    *report.LimitWindow
	}{{"5h", in.FiveHour}, {"7d", in.SevenDay}} {
		if w.w != nil {
			s += fmt.Sprintf(" %s=%v%%/%s", w.name, w.w.UsedPercent, w.w.Label)
			if w.w.ResetsAt != nil {
				s += "@" + w.w.ResetsAt.Format(time.RFC3339)
			}
		}
	}
	if in.CacheWarm != nil {
		s += fmt.Sprintf(" warm=%v", *in.CacheWarm)
	}
	if in.CacheTTLSeconds != nil {
		s += fmt.Sprintf(" ttl=%ds", *in.CacheTTLSeconds)
	}
	return s + "}"
}
