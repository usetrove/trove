package measure

import (
	"context"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// notifyWithin is how recently a session must have ended for its receipt to
// trigger a notification, so a gateway started after a long break stays quiet.
const notifyWithin = time.Hour

// Run rolls up finished sessions, writes their receipts and checks the
// guardrails once immediately and then every interval until ctx ends. It runs
// beside the gateway, never on the request path. Errors go to onErr (may be
// nil); a failure never stops the loop. onReceipt (may be nil) is called for
// each new receipt of a session that just ended, when the user has turned
// desktop notifications on.
func Run(ctx context.Context, db *store.DB, cfg func() config.Config, idle, interval time.Duration, onErr func(error), onReceipt func(Receipt)) {
	step := func() {
		c := cfg()
		fail := func(err error) {
			if err != nil && onErr != nil && ctx.Err() == nil {
				onErr(err)
			}
		}
		now := time.Now()
		_, err := RollupSessions(ctx, db, c.Measure, now, idle)
		fail(err)
		_, _, err = Evaluate(ctx, db, c.Measure, now)
		fail(err)
		rs, err := SweepReceipts(ctx, db, idle, now)
		fail(err)
		if onReceipt != nil && c.Notifications.Desktop {
			for _, r := range rs {
				if now.Sub(r.EndedAt) <= notifyWithin {
					onReceipt(r)
				}
			}
		}
	}
	step()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			step()
		}
	}
}
