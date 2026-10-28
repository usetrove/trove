// Command pricingage warns when bundled prices have not been verified recently.
// It prints GitHub Actions ::warning:: annotations and always exits 0: a stale
// price should be noticed at release time, not block a security fix.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/usetrove/trove/internal/pricing"
)

func main() {
	t, err := pricing.Default()
	if err != nil {
		fmt.Fprintf(os.Stderr, "::error::bundled pricing data is invalid: %v\n", err)
		os.Exit(1)
	}
	stale := t.Stale(time.Now(), pricing.MaxAge)
	if len(stale) == 0 {
		fmt.Printf("All %d bundled prices were verified within the last 30 days (data version %s).\n", len(t.Entries()), t.Version())
		return
	}
	for _, e := range stale {
		fmt.Printf("::warning file=internal/pricing/pricing.json::%s/%s (from %s) was last verified %s, more than 30 days ago. Re-check it against %s (see docs/pricing.md).\n",
			e.Provider, e.Model, e.EffectiveFrom, e.VerifiedAt, e.SourceURL)
	}
	fmt.Printf("%d of %d bundled prices are stale.\n", len(stale), len(t.Entries()))
}
