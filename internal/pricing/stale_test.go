package pricing

import "testing"

func TestStale(t *testing.T) {
	tb := fixtureTable(t, priceChangeFixture) // verified_at: 2026-01-01, 2026-02-01, 2026-02-01
	tests := []struct {
		now  string
		want int
	}{
		{"2026-01-31", 0}, // 30 days after 2026-01-01 is not older than 30 days
		{"2026-02-01", 1}, // 31 days: first entry is stale
		{"2026-03-03", 1}, // 30 days after 2026-02-01
		{"2026-03-04", 3}, // 31 days: all stale
		{"2025-12-01", 0}, // clock before the data is not stale
	}
	for _, tt := range tests {
		if got := len(tb.Stale(at(tt.now), MaxAge)); got != tt.want {
			t.Errorf("now %s: %d stale entries, want %d", tt.now, got, tt.want)
		}
	}
}
