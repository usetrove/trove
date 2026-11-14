package autopilot

import (
	"fmt"

	"github.com/usetrove/trove/internal/config"
)

// Notice is one desktop notification about an Autopilot change.
type Notice struct {
	Title, Body string
	AtMS        int64
}

// Notices returns the changes not yet announced that are worth a notification: an
// optimizer turned on, or rolled back. Moves between shadow and candidate are not.
func Notices(f config.AutopilotFile) []Notice {
	var out []Notice
	for _, e := range f.History {
		if e.AtMS <= f.NotifiedMS {
			continue
		}
		switch e.To {
		case config.AutopilotOn:
			out = append(out, Notice{"Trove Autopilot", fmt.Sprintf("Turned on %s from shadow evidence (estimated). Undo with `trove autopilot off`.", e.Optimizer), e.AtMS})
		case config.AutopilotRolledBack:
			out = append(out, Notice{"Trove Autopilot", fmt.Sprintf("Rolled %s back to shadow: %s. See `trove autopilot`.", e.Optimizer, e.Reason), e.AtMS})
		}
	}
	return out
}

// Announce sends the pending notices through send and records that it did, so a
// notice is sent once. It sends nothing when enabled is false (the user did not
// turn desktop notifications on) or send is nil, but still records the events as
// seen, so turning notifications on later does not replay old ones. Best effort:
// a failure here never affects a request.
func Announce(dir string, enabled bool, send func(title, body string)) error {
	f, err := config.LoadAutopilot(dir)
	if err != nil || len(f.History) == 0 {
		return err
	}
	newest := f.History[len(f.History)-1].AtMS
	if newest <= f.NotifiedMS {
		return nil
	}
	if enabled && send != nil {
		for _, n := range Notices(f) {
			send(n.Title, n.Body)
		}
	}
	f.NotifiedMS = newest
	return config.SaveAutopilot(dir, f)
}
