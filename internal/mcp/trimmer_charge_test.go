package mcp

import (
	"context"
	"testing"

	"github.com/usetrove/trove/internal/mcp/root"
	"github.com/usetrove/trove/internal/technique"
)

// Autopilot charges the output trimmer for the tool definitions it cannot work without (rule 15). The figure is
// declared in the technique registry; this keeps it equal to what the server really sends, so shortening or
// growing a tool description cannot leave Autopilot charging the wrong amount.
func TestTheTrimmersDeclaredChargeMatchesTheToolDefinitions(t *testing.T) {
	r, _ := root.New(t.TempDir())
	sz, err := MeasureSchema(context.Background(), Options{Root: r, Store: nil})
	if err != nil {
		t.Fatal(err)
	}
	o, ok := technique.OverheadOf("output_trimmer")
	if !ok {
		t.Fatal("output_trimmer declares no overhead")
	}
	const slack = 15 // tokens: the size is an estimate and descriptions change a little
	if diff := int(o.PerSession) - sz.EstimatedTokens; diff > slack || diff < -slack {
		t.Errorf("the registry charges the trimmer %d tokens a session but the tool definitions are about %d; update trimmerToolDefTokens in internal/technique/registry.go", o.PerSession, sz.EstimatedTokens)
	}
}
