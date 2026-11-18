package config

import (
	"strings"
	"testing"
)

func TestKeepWarmDefaultsMatchTheDecision(t *testing.T) {
	c := Default()
	if c.Gateway.KeepWarmMaxPings != 2 || c.Gateway.KeepWarmDailyCapUSD != 1 {
		t.Errorf("defaults: %d pings, $%v a day; docs/decisions/keep-warm.md says 2 and $1", c.Gateway.KeepWarmMaxPings, c.Gateway.KeepWarmDailyCapUSD)
	}
	if !IsGatewayBehavior(OptimizerKeepWarm) {
		t.Error("keep_warm must be a gateway behavior")
	}
	if c.OptimizerState(OptimizerKeepWarm) != StateOff {
		t.Error("keep_warm must be off by default")
	}
}

func TestKeepWarmLimitsCanOnlyBeLowered(t *testing.T) {
	for _, tc := range []struct {
		pings int
		cap   float64
		ok    bool
	}{
		{2, 1, true}, {1, 0.5, true}, {6, 1, true}, {7, 1, false}, {0, 1, false},
		{2, 1.01, false}, {2, 0, false}, {2, -1, false},
	} {
		c := Default()
		c.Gateway.KeepWarmMaxPings, c.Gateway.KeepWarmDailyCapUSD = tc.pings, tc.cap
		err := c.Validate()
		if (err == nil) != tc.ok {
			t.Errorf("pings %d cap %v: err %v, want ok=%v", tc.pings, tc.cap, err, tc.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "keep_warm") {
			t.Errorf("the error should name the setting: %v", err)
		}
	}
}
