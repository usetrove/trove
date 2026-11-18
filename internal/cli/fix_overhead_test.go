package cli

import (
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/fixer"
)

func TestFixDetailShowsResentTokens(t *testing.T) {
	f := &fixer.Fix{ID: "mcp-1", Kind: fixer.MCPPrune, Title: "t", Why: "w",
		ResentTokensPerRequest: 1100, ResentLabel: "measured", RequestsPerSession: 2, RequestsLabel: "measured"}
	got := renderFixDetail(f, fixer.Activity{})
	if want := "Re-sent on every request: 1100 tokens (measured), 2.0 requests per session (measured)."; !strings.Contains(got, want) {
		t.Fatalf("detail lacks %q:\n%s", want, got)
	}
	if strings.Contains(renderFixDetail(&fixer.Fix{ID: "x"}, fixer.Activity{}), "Re-sent") {
		t.Fatal("no figure, no line")
	}
}
