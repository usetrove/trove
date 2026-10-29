package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/usetrove/trove/internal/optimize"
)

// Pins persists per-session optimizer settings so a gateway restart in the
// middle of a session keeps the session's optimizer states (CLAUDE.md rule 5).
// It satisfies optimize.PinStore.
type Pins struct{ db *DB }

// Pins returns the pin store for d.
func (d *DB) Pins() *Pins { return &Pins{db: d} }

// Load returns the pin saved for sessionKey, if any.
func (p *Pins) Load(ctx context.Context, sessionKey string) (optimize.Pin, bool, error) {
	var states, agents string
	var bpt float64
	var at int64
	err := p.db.QueryRowContext(ctx,
		`SELECT states, custom_agents, bytes_per_token, pinned_at FROM session_pins WHERE session_key = ?`, sessionKey).
		Scan(&states, &agents, &bpt, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return optimize.Pin{}, false, nil
	}
	if err != nil {
		return optimize.Pin{}, false, err
	}
	pin := optimize.Pin{SessionKey: sessionKey, BytesPerToken: bpt, PinnedAt: time.UnixMilli(at).UTC()}
	if err := json.Unmarshal([]byte(states), &pin.States); err != nil {
		return optimize.Pin{}, false, err
	}
	if err := json.Unmarshal([]byte(agents), &pin.CustomAgents); err != nil {
		return optimize.Pin{}, false, err
	}
	return pin, true, nil
}

// Save stores pin. The first pin for a session wins: a later Save for the same
// session is ignored, so a session's settings can never change once pinned.
func (p *Pins) Save(ctx context.Context, pin optimize.Pin) error {
	states, err := json.Marshal(pin.States)
	if err != nil {
		return err
	}
	agents, err := json.Marshal(pin.CustomAgents)
	if err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO session_pins (session_key, states, custom_agents, bytes_per_token, pinned_at) VALUES (?,?,?,?,?)`,
		pin.SessionKey, string(states), string(agents), pin.BytesPerToken, pin.PinnedAt.UnixMilli())
	return err
}
