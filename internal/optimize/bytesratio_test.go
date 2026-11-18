package optimize

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

func TestSessionRatioFallsBackToDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    Session
		want float64
	}{
		{"unset", Session{}, DefaultBytesPerToken},
		{"calibrated", Session{BytesPerToken: 4.2, BytesPerTokenN: 80}, 4.2},
		{"nonsense is ignored", Session{BytesPerToken: -1}, DefaultBytesPerToken},
	} {
		if got := tc.s.bytesPerToken(); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEstTokensAt(t *testing.T) {
	for _, tc := range []struct {
		bytes int64
		bpt   float64
		want  int64
	}{
		{3500, 3.5, 1000}, {3501, 3.5, 1001}, {4000, 4, 1000}, {4001, 4, 1001}, {0, 4, 0}, {7, 0, 2}, // 0 means the default
	} {
		if got := EstTokensAt(tc.bytes, tc.bpt); got != tc.want {
			t.Errorf("EstTokensAt(%d, %v) = %d, want %d", tc.bytes, tc.bpt, got, tc.want)
		}
	}
	if EstTokensAt(12345, DefaultBytesPerToken) != EstTokens(12345) {
		t.Error("the default ratio must give the same answer as EstTokens")
	}
}

// A hook's estimate uses the session's own ratio, so it moves with calibration.
func TestReadLimitEstimateUsesSessionRatio(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 80_000)), 0o600); err != nil {
		t.Fatal(err)
	}
	call := ToolCall{Name: "Read", Input: []byte(`{"file_path":` + quote(path) + `}`)}
	_, def := ReadLimit{}.TransformInput(context.Background(), call, Session{Mode: config.StateShadow})
	_, cal := ReadLimit{}.TransformInput(context.Background(), call, Session{Mode: config.StateShadow, BytesPerToken: 5, BytesPerTokenN: 60})
	if def.TokensRemoved != EstTokens(def.Decision.Bytes) {
		t.Fatalf("default session: %d tokens for %d bytes", def.TokensRemoved, def.Decision.Bytes)
	}
	if want := EstTokensAt(cal.Decision.Bytes, 5); cal.TokensRemoved != want || cal.Decision.Tokens != want {
		t.Fatalf("calibrated session: report %d, decision %d, want %d", cal.TokensRemoved, cal.Decision.Tokens, want)
	}
	if cal.TokensRemoved >= def.TokensRemoved {
		t.Errorf("more bytes per token must mean fewer tokens: %d vs %d", cal.TokensRemoved, def.TokensRemoved)
	}
}

func TestToolOutputEstimateUsesSessionRatio(t *testing.T) {
	e := toolEngine(t, 100, toolStub{name: "x", outFn: shorten})
	in := ToolOutput{Parts: []Part{{Text: strings.Repeat("a", 20000)}}}
	s := stateOf(config.StateShadow)
	_, def := e.TransformToolOutput(context.Background(), ToolCall{Name: "Grep"}, in, s)
	s.BytesPerToken, s.BytesPerTokenN = 5, 60
	_, cal := e.TransformToolOutput(context.Background(), ToolCall{Name: "Grep"}, in, s)
	if len(def) != 1 || len(cal) != 1 || def[0].BytesRemoved != cal[0].BytesRemoved {
		t.Fatalf("setup: %+v %+v", def, cal)
	}
	if want := EstTokensAt(int64(cal[0].BytesRemoved), 5); cal[0].TokensRemoved != want {
		t.Errorf("calibrated tokens = %d, want %d", cal[0].TokensRemoved, want)
	}
	if def[0].TokensRemoved != EstTokens(int64(def[0].BytesRemoved)) {
		t.Errorf("default tokens = %d", def[0].TokensRemoved)
	}
}
