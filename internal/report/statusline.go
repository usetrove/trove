package report

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway/session"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/store"
)

// Status line output formats.
const (
	FormatClaude = "claude" // ANSI color, for Claude Code's statusLine setting
	FormatTmux   = "tmux"   // tmux #[...] style codes
	FormatShell  = "shell"  // plain text for a shell prompt
)

// Status is the data behind the one-line status. Every dollar figure is an
// estimate from list prices.
type Status struct {
	Period   string         `json:"period"`
	Requests int            `json:"requests"`
	Spend    measure.Figure `json:"spend_usd"`
	Saved    measure.Figure `json:"saved_usd"`
	Budget   *Pacing        `json:"budget,omitempty"`
	// Session is the cost of the session the status line runs in, when known.
	Session *SessionCost `json:"session,omitempty"`
}

// SessionCost is one session's spend so far (estimated; calls that could not be
// priced count as zero, so it is a lower bound).
type SessionCost struct {
	Cost  measure.Figure `json:"cost_usd"`
	Calls int            `json:"calls"`
}

// BuildStatus reads the small per-day summary the gateway keeps current
// (usage_daily) and, if sessionID is a Claude Code session id, that session's
// row. It never scans the requests table, so it stays fast however much history
// there is.
func BuildStatus(ctx context.Context, db *store.DB, cfg config.Config, now time.Time, sessionID string) (Status, error) {
	name := Month
	if cfg.Budget.CapUSD > 0 {
		name = PeriodName(cfg.Budget.Period)
	}
	from := StartOf(name, now)
	to := EndOf(name, from)
	st := Status{Period: name, Spend: measure.Figure{Label: measure.Estimated}, Saved: measure.Figure{Label: measure.Estimated}}

	var reqs, unpriced int
	var spend, saved float64
	err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(requests),0), COALESCE(SUM(unpriced),0), COALESCE(SUM(spend_usd),0), COALESCE(SUM(saved_usd),0)
		FROM usage_daily WHERE day >= ? AND day <= ?`, store.DayKey(from), store.DayKey(now)).Scan(&reqs, &unpriced, &spend, &saved)
	if err != nil {
		return st, err
	}
	st.Requests = reqs
	st.Spend = measure.Figure{Value: round6(spend), Label: measure.Estimated, N: reqs - unpriced, Unpriced: unpriced}
	st.Saved = measure.Figure{Value: round6(saved), Label: measure.Estimated, N: reqs}
	if cfg.Budget.CapUSD > 0 {
		st.Budget = Pace(cfg.Budget, name, st.Spend, from, to, now)
	}

	if sessionID != "" {
		base := session.ClaudeCodeKey(sessionID)
		var cost float64
		var calls int
		// The key range covers the base key and its "~n" idle-gap successors.
		err := db.QueryRowContext(ctx, `SELECT total_cost_usd, request_count FROM sessions
			WHERE session_key >= ? AND session_key < ? ORDER BY ended_at DESC LIMIT 1`, base, base+"\x7f").Scan(&cost, &calls)
		if err == nil {
			st.Session = &SessionCost{Cost: measure.Figure{Value: round6(cost), Label: measure.Estimated, N: calls}, Calls: calls}
		}
	}
	return st, nil
}

// RenderStatus is the one-line text, for example:
//
//	~$612 / $1,500 · 41% used · 48% of month · on pace · session ~$3.20 · saved ~$47
//
// "~" marks an estimate; "+" after a dollar amount means some calls could not be
// priced, so it is at least that much.
func RenderStatus(s Status, format string, color bool) string {
	if s.Requests == 0 && s.Budget == nil && s.Session == nil {
		return "Trove: no usage yet"
	}
	var parts []string
	spend := Est(s.Spend)
	if s.Spend.Unpriced > 0 {
		spend += "+"
	}
	noun := periodNoun(s.Period)
	if b := s.Budget; b != nil {
		parts = append(parts, spend+" / "+FormatUSD(b.CapUSD),
			FormatPercent(b.PercentUsed)+" used",
			FormatPercent(b.PercentElapsed)+" of "+noun,
			styled(paceWord(b.Status), b.Status, format, color))
	} else {
		parts = append(parts, spend+" this "+noun)
	}
	if s.Session != nil {
		parts = append(parts, "session "+Est(s.Session.Cost))
	}
	if s.Saved.Value >= 0.01 {
		parts = append(parts, "saved "+Est(s.Saved))
	}
	return strings.Join(parts, " · ")
}

func paceWord(status string) string {
	switch status {
	case PaceOver:
		return "over cap"
	case PaceNearCap:
		return "near cap"
	case PaceAhead:
		return "ahead of pace"
	case PaceOnPace:
		return "on pace"
	case PaceUnder:
		return "under pace"
	}
	return "too early to judge pace"
}

// styled colors the pace word: amber when ahead of pace, red near or over the cap.
func styled(word, status, format string, color bool) string {
	if !color || format == FormatShell {
		return word
	}
	var ansi, tmux string
	switch status {
	case PaceAhead:
		ansi, tmux = "33", "yellow"
	case PaceNearCap, PaceOver:
		ansi, tmux = "31", "red"
	default:
		return word
	}
	if format == FormatTmux {
		return fmt.Sprintf("#[fg=%s]%s#[default]", tmux, word)
	}
	return "\x1b[" + ansi + "m" + word + "\x1b[0m"
}
