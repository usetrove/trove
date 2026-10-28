package pricing

import "time"

// MaxAge is how old an entry's verified_at may be at release time before CI warns.
const MaxAge = 30 * 24 * time.Hour

// Stale returns the entries whose verified_at is more than maxAge before now.
func (t *Table) Stale(now time.Time, maxAge time.Duration) []Entry {
	var out []Entry
	for _, e := range t.Entries() {
		if e.VerifiedAt != nil && now.Sub(e.VerifiedAt.Time) > maxAge {
			out = append(out, e)
		}
	}
	return out
}
