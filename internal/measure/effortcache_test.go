package measure

import (
	"context"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/store"
)

// effortSession makes n consecutive requests 10 s apart. The effort alternates when
// alternate is true. readOnChange is what a turn reads after an effort change, as a
// share of the previous turn's cached prefix; a turn without a change reads it all.
func effortSession(key, model string, n int, alternate bool, readOnChange float64) []effortRow {
	var out []effortRow
	cached := int64(20000)
	for i := 0; i < n; i++ {
		eff := "high"
		if alternate && i%2 == 1 {
			eff = "low"
		}
		r := effortRow{session: key, model: model, effort: eff, ts: int64(i) * 10_000, read: cached, write: 500}
		if i > 0 && out[i-1].effort != eff {
			r.read = int64(readOnChange * float64(cached))
			r.write = cached - r.read + 500 // what was not read back is written again
		}
		out = append(out, r)
		cached = r.read + r.write
	}
	return out
}

func TestEffortCacheVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name         string
		readOnChange float64
		changedN     int
		want         string
	}{
		{"cache lost on change", 0, 12, EffortBreaks},
		{"cache kept on change", 1, 12, EffortKeeps},
		{"too few changes", 0, 3, EffortInsufficient},
	} {
		var rs []effortRow
		rs = append(rs, effortSession("a", "m1", tc.changedN, true, tc.readOnChange)...)
		rs = append(rs, effortSession("b", "m1", 10, false, 1)...) // control
		got := effortCache(rs)
		if len(got) != 1 || got[0].Verdict != tc.want || got[0].Label != Measured {
			t.Errorf("%s: %+v, want %s", tc.name, got, tc.want)
		}
	}
}

func TestEffortCacheIgnoresLongGapsAndOtherModels(t *testing.T) {
	rs := []effortRow{
		{session: "a", model: "m1", effort: "high", ts: 0, read: 0, write: 5000},
		{session: "a", model: "m1", effort: "low", ts: (5 * time.Minute).Milliseconds(), read: 0, write: 5000}, // cache expired anyway
		{session: "a", model: "m2", effort: "low", ts: (5 * time.Minute).Milliseconds() + 1000, read: 0, write: 5000},
	}
	for _, m := range effortCache(rs) {
		if m.ChangedPairs != 0 || m.ControlPairs != 0 {
			t.Errorf("%+v: a gap past the cache lifetime or a model switch is not an effort pair", m)
		}
	}
}

func TestEffortCacheCheckReadsRecordedEffort(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	base := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	for i, eff := range []string{"high", "low", "bogus"} {
		_, err := db.ExecContext(context.Background(), `INSERT INTO requests (ts, session_key, provider, model, status_code, cache_read_tokens, cache_write_tokens, effort)
			VALUES (?, 's', 'anthropic', 'm', 200, 3000, 100, ?)`, base.Add(time.Duration(i)*10*time.Second).UnixMilli(), eff)
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := EffortCacheCheck(context.Background(), db, base, base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChangedPairs != 2 || got[0].Verdict != EffortInsufficient {
		t.Errorf("got %+v", got)
	}
}
