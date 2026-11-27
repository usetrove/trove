package autopilot

import (
	"context"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/optimize"
)

// thinking_clear may leave shadow only on a net-positive estimate; every other optimizer is unaffected.
func TestNetEstimateBlocker(t *testing.T) {
	db, dir := open(t)
	why, err := netEstimateBlocker(context.Background(), db, dir, optimize.OptimizerThinkingClear, hookNow)
	if err != nil || !strings.Contains(why, "needs 20 requests and has 0") {
		t.Fatalf("%q %v", why, err)
	}
	if why, err := netEstimateBlocker(context.Background(), db, dir, optimize.OptimizerEffortToolTurns, hookNow); why != "" || err != nil {
		t.Fatalf("%q %v", why, err)
	}
}
