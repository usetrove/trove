package cli

import (
	"strings"
	"testing"
	"time"
)

func TestCheckPricingAge(t *testing.T) {
	// Far in the future every verified_at is stale; at the data version's own date it is not.
	if c := checkPricing(time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)); c.Status != statusWarn || !strings.Contains(c.Detail, "30 days") || c.Fix == "" {
		t.Errorf("want a warn with a fix, got %+v", c)
	}
	if c := checkPricing(time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)); c.Status != statusOK {
		t.Errorf("want ok right after verification, got %+v", c)
	}
}
