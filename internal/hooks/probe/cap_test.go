package probe

import "testing"

// Each row: the run's cap, the number of tools, and the share each tool may spend. The shares must add up to
// no more than the cap, and a cap too small for a cent per tool is refused.
func TestPerToolCapNeverAddsUpAboveTheCap(t *testing.T) {
	cases := []struct {
		name    string
		max     float64
		tools   int
		want    float64
		wantErr bool
	}{
		{"four tools, 0.10 cap (rounding to nearest would give 0.12)", 0.10, 4, 0.02, false},
		{"four tools, 0.50 cap", 0.50, 4, 0.12, false},
		{"four tools, 0.25 cap", 0.25, 4, 0.06, false},
		{"one tool gets the whole cap", 0.50, 1, 0.50, false},
		{"three tools, 1.00 cap", 1.00, 3, 0.33, false},
		{"a cap of one cent for four tools is refused", 0.01, 4, 0, true},
		{"a cap of zero is refused", 0, 4, 0, true},
		{"no tools", 0.50, 0, 0, true},
	}
	for _, tc := range cases {
		got, err := perToolCap(tc.max, tc.tools)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: error = %v, want error %v", tc.name, err, tc.wantErr)
			continue
		}
		if tc.wantErr {
			continue
		}
		if got != tc.want {
			t.Errorf("%s: share = %v, want %v", tc.name, got, tc.want)
		}
		if sum := got * float64(tc.tools); sum > tc.max+1e-9 {
			t.Errorf("%s: %d shares of %v add up to %v, above the cap %v", tc.name, tc.tools, got, sum, tc.max)
		}
	}
}
