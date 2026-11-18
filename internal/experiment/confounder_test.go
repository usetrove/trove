package experiment

import (
	"context"
	"strings"
	"testing"
)

func TestMixedConfoundersAreFlagged(t *testing.T) {
	ctx := context.Background()
	flat := profile{cost: 1, turns: 20, errs: 2}
	tests := []struct {
		name      string
		tagEvery  int // tag every Nth session; 0 tags none, 1 tags all
		wantMixed bool
	}{
		{"none tagged", 0, false},
		{"all tagged", 1, false},
		{"some tagged", 3, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			arms := simulate(t, db, "opt", 60, flat, flat, 7)
			i := 0
			for k := range arms {
				i++
				if tc.tagEvery > 0 && i%tc.tagEvery == 0 {
					if _, err := db.ExecContext(ctx, `UPDATE session_pins SET confounders = '["brevity_rules"]' WHERE session_key = ?`, k); err != nil {
						t.Fatal(err)
					}
				}
			}
			res, err := Analyze(ctx, db, "opt", testCfg(), Priors{})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Confounders != nil; got != tc.wantMixed {
				t.Fatalf("mixed note present = %v, want %v", got, tc.wantMixed)
			}
			if tc.wantMixed {
				n := res.Confounders
				if n.Tagged == 0 || n.Untagged == 0 || len(n.Tags) != 1 || n.Tags[0] != "brevity_rules" || !strings.Contains(n.Note, "mixed comparison") {
					t.Errorf("bad note: %+v", n)
				}
			}
		})
	}
}
