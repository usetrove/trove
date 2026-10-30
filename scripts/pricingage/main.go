// Command pricingage warns when bundled prices have not been verified recently.
// By default it prints GitHub Actions ::warning:: annotations and always exits 0:
// a stale price should be noticed at release time, not block a security fix.
// For a version release use --strict --max-age-days 7, which exits 1 instead.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/usetrove/trove/internal/pricing"
)

func main() {
	days := flag.Int("max-age-days", int(pricing.MaxAge/(24*time.Hour)), "warn about prices verified more than this many days ago")
	strict := flag.Bool("strict", false, "exit 1 when any price is stale (for release gates)")
	flag.Parse()
	if *days < 1 {
		fmt.Fprintln(os.Stderr, "::error::--max-age-days must be at least 1")
		os.Exit(2)
	}
	maxAge := time.Duration(*days) * 24 * time.Hour

	t, err := pricing.Default()
	if err != nil {
		fmt.Fprintf(os.Stderr, "::error::bundled pricing data is invalid: %v\n", err)
		os.Exit(1)
	}
	stale := t.Stale(time.Now(), maxAge)
	if len(stale) == 0 {
		fmt.Printf("All %d bundled prices were verified within the last %d days (data version %s).\n", len(t.Entries()), *days, t.Version())
		return
	}
	for _, e := range stale {
		fmt.Printf("::warning file=internal/pricing/pricing.json::%s/%s (from %s) was last verified %s, more than %d days ago. Re-check it against %s (see docs/pricing.md).\n",
			e.Provider, e.Model, e.EffectiveFrom, e.VerifiedAt, *days, e.SourceURL)
	}
	fmt.Printf("%d of %d bundled prices are stale.\n", len(stale), len(t.Entries()))
	if *strict {
		os.Exit(1)
	}
}
