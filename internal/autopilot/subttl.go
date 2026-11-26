package autopilot

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/fixer"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// The settings policy (settings-v1): Autopilot sets the subagent prompt cache lifetime to one hour only when
// this user's own history shows it pays, and takes it back off when it stops paying. docs/autopilot.md,
// "Settings policy".
//
// Facts, read on 2026-11-24 (docs/techniques.md, "What was verified", S10):
//   - subagentPromptCacheTtl ("5m" or "1h", Claude Code v2.1.242 or later) sets the lifetime of the requests
//     outside the main conversation; the default is five minutes (code.claude.com/docs/en/prompt-caching).
//   - A five-minute write costs 1.25x the base input price, a one-hour write 2x, a read 0.1x or less
//     (platform.claude.com/docs/en/build-with-claude/prompt-caching). Trove's price table has the per-model figures.
//   - How a one-hour write counts toward a subscription's 5-hour and weekly limits is not documented. When this
//     user's limit fit has per-type weights (internal/measure/limitfit) it is used, labeled "estimated from your
//     meter"; otherwise API list prices are, labeled "estimated from API prices".
//
// Rules it keeps (PRINCIPLES.md): only the one setting the policy names, only after the user agreed to the
// policy (rule 6); never over a value the user set (rule 6); written and removed only at a session end with
// no other agent session active, so no running session sees it change (rule 5); a failure changes nothing
// (rule 4); every figure is an estimate (rules 6 and 9); numbers only in autopilot.json (rule 7).

// SubTTLName is the setting's name in autopilot.json and in the fixes table.
const SubTTLName = fixer.CacheTTLSubagents

// Settings-policy thresholds. Placeholders until a benchmark sets them; the API-price margin and minimum are
// the cache_ttl suggestion's own (docs/optimize.md).
const (
	subTTLLookbackDays  = 30
	subTTLTripwireDays  = 14
	subTTLCooldownDays  = 30
	subTTLMaxAttempts   = 2
	subTTLEvalEvery     = 6 * time.Hour
	subTTLMinRequests   = 100
	subTTLMinSessions   = 5
	subTTLMinRebuilds   = 5
	subTTLMargin        = 1.25
	subTTLMinUSD        = 1.0
	subTTLMaxUnsplit    = 0.2
	subTTLTripwireMinRq = 30
)

// subTTLEstimate is one judgement: the figures shown, plus the parts the rules need.
type subTTLEstimate struct {
	config.AutopilotSettingEstimate
	gainUSD, costUSD float64
	unsplitShare     float64
	unpriced         bool
}

type subTTLPrice struct{ w5, w1, rd float64 } // dollars per token

func subTTLPrices(tbl *pricing.Table, model string, at time.Time) *subTTLPrice {
	if tbl == nil {
		return nil
	}
	e, _ := tbl.Lookup(pricing.Anthropic, model, at)
	if e == nil || e.Standard.CacheRead == nil {
		return nil
	}
	w5, ok5 := e.Standard.CacheWrite["5m"]
	w1, ok1 := e.Standard.CacheWrite["1h"]
	if !ok5 || !ok1 {
		return nil
	}
	f5, e5 := strconv.ParseFloat(w5.String(), 64)
	f1, e1 := strconv.ParseFloat(w1.String(), 64)
	fr, er := strconv.ParseFloat(e.Standard.CacheRead.String(), 64)
	if e5 != nil || e1 != nil || er != nil {
		return nil
	}
	return &subTTLPrice{w5: f5 / 1e6, w1: f1 / 1e6, rd: fr / 1e6}
}

// estimateSubTTL is the net of the one-hour lifetime over the five-minute one for the subagent bucket, as a
// saving (positive = one hour is cheaper).
//
//   - onHour false: the history ran on five minutes. The 5-to-60-minute rebuilds would have been reads, and
//     every other five-minute write would have cost the one-hour price.
//   - onHour true: the history ran on one hour. The reads after a 5-to-60-minute pause would have been
//     rewrites at five minutes, and every one-hour write would have cost the five-minute price.
//
// Both are "the one-hour world minus the five-minute world": A tokens written at one hour instead of five
// minutes, and G tokens read instead of rewritten. It assumes a lost entry is rewritten in full.
func estimateSubTTL(s analyze.SubTTL, onHour bool, tbl *pricing.Table, at time.Time, fit *limitfit.Result, days int) subTTLEstimate {
	tot := s.Total()
	est := subTTLEstimate{AutopilotSettingEstimate: config.AutopilotSettingEstimate{
		Basis: limitfit.BasisAPIPrices, Label: "estimated", Requests: s.Requests, Sessions: s.Sessions, WindowDays: days,
		GapRebuilds: tot.GapRebuilds, GapReads: tot.GapReads}}
	if w := tot.Write5m + tot.Write1h + tot.WriteNA; w > 0 {
		est.unsplitShare = float64(tot.WriteNA) / float64(w)
	}
	models := make([]string, 0, len(s.ByModel))
	for m := range s.ByModel {
		models = append(models, m)
	}
	sort.Strings(models)
	var delta limitfit.Delta
	top, unpricedReqs := 0, 0
	for _, model := range models {
		m := s.ByModel[model]
		a, g := m.Write1h, m.GapReadTokens
		if !onHour {
			g = min(m.GapRebuildTokens, m.Write5m)
			a = m.Write5m - g
		}
		delta.Tokens[limitfit.TWrite1h] += a
		delta.Tokens[limitfit.TWrite5m] -= a + g
		delta.Tokens[limitfit.TCacheRead] += g
		if m.Requests > top {
			top, delta.Model = m.Requests, model
		}
		p := subTTLPrices(tbl, model, at)
		if p == nil {
			unpricedReqs += m.Requests
			continue
		}
		est.gainUSD += float64(g) * (p.w5 - p.rd)
		est.costUSD += float64(a) * (p.w1 - p.w5)
	}
	est.unpriced = s.Requests > 0 && unpricedReqs > s.Requests/4
	est.NetUSD = est.gainUSD - est.costUSD
	subscription := s.APIKey*2 < s.Requests
	if fit != nil && subscription && fit.FiveHour.Tier == limitfit.TierFit && delta.Tokens != ([limitfit.NTypes]int64{}) {
		if e := fit.Percent(delta); e != nil && e.FiveHour != nil && e.FiveHour.Basis == limitfit.BasisFit {
			r := e.FiveHour.Range // the cost of the one-hour world; the saving is its negative
			est.Basis, est.NetPct5h, est.NetPct5hLo, est.NetPct5hHi = limitfit.BasisFit, -r.Point, -r.Hi, -r.Lo
		}
	}
	return est
}

// recommend says whether the five-minute history clearly shows the one-hour lifetime would pay.
func (e subTTLEstimate) recommend() (bool, string) {
	switch {
	case e.Requests < subTTLMinRequests || e.Sessions < subTTLMinSessions:
		return false, fmt.Sprintf("not enough subagent history yet (%d requests in %d sessions; needs %d in %d)", e.Requests, e.Sessions, subTTLMinRequests, subTTLMinSessions)
	case e.unsplitShare > subTTLMaxUnsplit:
		return false, "the history does not say which cache lifetime most writes used"
	case e.unpriced:
		return false, "Trove has no cache prices for most of the models used"
	case e.GapRebuilds < subTTLMinRebuilds:
		return false, fmt.Sprintf("only %d subagent cache rebuilds after a 5-to-60-minute pause; needs %d", e.GapRebuilds, subTTLMinRebuilds)
	}
	if e.Basis == limitfit.BasisFit {
		if e.NetPct5hLo > 0 {
			return true, fmt.Sprintf("estimated from your meter: saves %.2f%% of the 5-hour limit over %d days (90%% interval %.2f to %.2f)", e.NetPct5h, e.WindowDays, e.NetPct5hLo, e.NetPct5hHi)
		}
		return false, fmt.Sprintf("estimated from your meter: %.2f%% of the 5-hour limit over %d days, not clearly a saving (90%% interval %.2f to %.2f)", e.NetPct5h, e.WindowDays, e.NetPct5hLo, e.NetPct5hHi)
	}
	if e.gainUSD > e.costUSD*subTTLMargin && e.NetUSD >= subTTLMinUSD {
		return true, fmt.Sprintf("estimated from API prices: about $%.2f less over %d days ($%.2f of rewrites avoided, $%.2f one-hour premium)", e.NetUSD, e.WindowDays, e.gainUSD, e.costUSD)
	}
	return false, fmt.Sprintf("estimated from API prices: about $%.2f over %d days, not clearly a saving ($%.2f of rewrites avoided, $%.2f one-hour premium)", e.NetUSD, e.WindowDays, e.gainUSD, e.costUSD)
}

// reverse says whether the one-hour history shows the lifetime stopped paying. Reverting is the safe
// direction, so the point estimate decides.
func (e subTTLEstimate) reverse() (bool, string) {
	if e.Requests < subTTLTripwireMinRq {
		return false, fmt.Sprintf("kept: only %d subagent requests in the last %d days, too few to judge", e.Requests, e.WindowDays)
	}
	if e.Basis == limitfit.BasisFit {
		if e.NetPct5h < 0 {
			return true, fmt.Sprintf("estimated from your meter: cost %.2f%% of the 5-hour limit over %d days", -e.NetPct5h, e.WindowDays)
		}
		return false, fmt.Sprintf("kept: estimated from your meter, saves %.2f%% of the 5-hour limit over %d days", e.NetPct5h, e.WindowDays)
	}
	if e.NetUSD < 0 {
		return true, fmt.Sprintf("estimated from API prices: cost about $%.2f more over %d days ($%.2f one-hour premium, $%.2f of rewrites avoided)", -e.NetUSD, e.WindowDays, e.costUSD, e.gainUSD)
	}
	return false, fmt.Sprintf("kept: estimated from API prices, about $%.2f less over %d days", e.NetUSD, e.WindowDays)
}

// SettingsDeps is what the settings step reads and writes beyond autopilot.json.
type SettingsDeps struct {
	DB      *store.DB
	Fixer   *fixer.Env // ClaudeDir, ProjectDir (the ending session's folder), DataDir, Getenv, Now
	Pricing *pricing.Table
	Fit     *limitfit.Result // nil when there is no fit
	// SessionID is the raw id of the session that is ending, held in memory only, so its own transcript is
	// not taken for another running session.
	SessionID string
}

// SettingsDue reports whether AdvanceSettings has anything to do now, so a caller can skip reading the
// history when it has not: the policy is on and the last judgement is old enough, or a change Autopilot made
// must be taken back.
func SettingsDue(f config.AutopilotFile, now time.Time) bool {
	st, ok := f.Settings[SubTTLName]
	if !f.SettingsActive() {
		return ok && st.State == config.AutopilotOn
	}
	return !ok || st.EvalMS == 0 || now.Sub(time.UnixMilli(st.EvalMS)) >= subTTLEvalEvery
}

// AdvanceSettings judges the settings the settings policy names, at a session end. It returns whether
// autopilot.json changed. It does nothing unless the user agreed to the policy.
func AdvanceSettings(ctx context.Context, dir string, d SettingsDeps, now time.Time) (bool, error) {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return false, err
	}
	if !f.SettingsActive() {
		return withdrawSubTTL(ctx, dir, d, f, now)
	}
	st, ok := f.Settings[SubTTLName]
	if !ok {
		st = config.AutopilotSetting{AutopilotEntry: config.AutopilotEntry{State: config.AutopilotShadow, SinceMS: now.UnixMilli(), Reason: "collecting estimates"}}
	}
	if st.EvalMS != 0 && now.Sub(time.UnixMilli(st.EvalMS)) < subTTLEvalEvery {
		return false, nil
	}
	next, ev := judgeSubTTL(ctx, d, st, now)
	if f.Settings == nil {
		f.Settings = map[string]config.AutopilotSetting{}
	}
	f.Settings[SubTTLName] = next
	if ev != nil {
		f.Record(*ev)
	}
	return true, config.SaveAutopilot(dir, f)
}

func judgeSubTTL(ctx context.Context, d SettingsDeps, st config.AutopilotSetting, now time.Time) (config.AutopilotSetting, *config.AutopilotEvent) {
	event := func(from, to, why string) *config.AutopilotEvent {
		return &config.AutopilotEvent{AtMS: now.UnixMilli(), Optimizer: SubTTLName, From: from, To: to, Reason: why}
	}
	if st.State == config.AutopilotOn {
		own, err := fixer.FixInPlace(ctx, d.Fixer, st.FixID)
		if err != nil {
			// Trove cannot tell whether its change is still there: change nothing, try again later.
			st.Reason = "on; cannot read Trove's record of the change, so nothing was judged"
			return st, nil
		}
		if !own {
			// The user undid it: their choice. Never set it again.
			st.State, st.FixID, st.Attempts, st.SinceMS, st.EvalMS = config.AutopilotOff, "", subTTLMaxAttempts, now.UnixMilli(), now.UnixMilli()
			st.Reason = "you undid Autopilot's change, so it will not set this again"
			return st, event(config.AutopilotOn, config.AutopilotOff, st.Reason)
		}
		left := time.UnixMilli(st.SinceMS).AddDate(0, 0, subTTLTripwireDays).Sub(now)
		if left > 0 {
			st.EvalMS = now.UnixMilli()
			st.Reason = fmt.Sprintf("on; measuring, the first check is in %d day(s)", int(left.Hours()/24)+1)
			return st, nil
		}
		est, err := subTTLHistory(ctx, d, true, subTTLTripwireDays, now)
		if err != nil {
			st.Reason = "on; cannot read the Claude Code history, so nothing was judged"
			return st, nil
		}
		st.Estimate = &est.AutopilotSettingEstimate
		back, why := est.reverse()
		if !back {
			st.EvalMS, st.Reason = now.UnixMilli(), why
			return st, nil
		}
		if busy, wait := fixer.OtherSessionActive(ctx, d.Fixer, d.SessionID); busy {
			st.Reason = "would set it back to the default (" + why + "), waiting because " + wait
			return st, nil // not stamped: try again at the next session end
		}
		if err := fixer.RevertSubagentTTL(ctx, d.Fixer, st.FixID, "1h"); err != nil {
			st.Reason = "would set it back to the default, but could not edit your Claude Code user settings. Run `trove undo " + st.FixID + "` to remove it yourself"
			return st, nil
		}
		st.State, st.FixID, st.SinceMS, st.EvalMS = config.AutopilotShadow, "", now.UnixMilli(), now.UnixMilli()
		st.Reason = "set back to the default: " + why
		return st, event(config.AutopilotOn, config.AutopilotShadow, st.Reason)
	}

	st.EvalMS = now.UnixMilli()
	if why := fixer.SubTTLUserChoice(ctx, d.Fixer, ""); why != "" {
		st.Reason = "left alone: " + why
		return st, nil
	}
	if st.Attempts >= subTTLMaxAttempts {
		st.Reason = fmt.Sprintf("left alone: it was turned back off %d time(s); set it yourself if you want it", st.Attempts)
		return st, nil
	}
	if st.Attempts > 0 {
		if until := time.UnixMilli(st.SinceMS).AddDate(0, 0, subTTLCooldownDays); now.Before(until) {
			st.Reason = fmt.Sprintf("waiting %d more day(s) after it was turned back off", int(until.Sub(now).Hours()/24)+1)
			return st, nil
		}
	}
	est, err := subTTLHistory(ctx, d, false, subTTLLookbackDays, now)
	if err != nil {
		st.Reason = "cannot read the Claude Code history, so nothing was judged"
		return st, nil
	}
	st.Estimate = &est.AutopilotSettingEstimate
	ok, why := est.recommend()
	if !ok {
		st.Reason = why
		return st, nil
	}
	if busy, wait := fixer.OtherSessionActive(ctx, d.Fixer, d.SessionID); busy {
		st.EvalMS = 0 // try again at the next session end
		st.Reason = "would set the one-hour lifetime (" + why + "), waiting because " + wait
		return st, nil
	}
	var perSession *float64
	if est.Basis == limitfit.BasisAPIPrices && est.Sessions > 0 {
		v := est.NetUSD / float64(est.Sessions)
		perSession = &v
	}
	id, err := fixer.SetSubagentTTL(ctx, d.Fixer, "1h", "Autopilot (settings-v1): "+why, perSession)
	if err != nil {
		st.Reason = "would set the one-hour lifetime, but could not edit your Claude Code user settings safely (for example, they are not plain JSON)"
		return st, nil
	}
	from := st.State
	st.State, st.FixID, st.SinceMS = config.AutopilotOn, id, now.UnixMilli()
	st.Attempts++
	st.Reason = "set subagentPromptCacheTtl to 1h for new sessions: " + why
	return st, event(from, config.AutopilotOn, st.Reason)
}

// subTTLHistory reads the subagent history of the last days and estimates it.
func subTTLHistory(ctx context.Context, d SettingsDeps, onHour bool, days int, now time.Time) (subTTLEstimate, error) {
	if d.DB == nil {
		return subTTLEstimate{}, fmt.Errorf("no database")
	}
	reqs, err := measure.HistoryLedger(ctx, d.DB, measure.Window{From: now.AddDate(0, 0, -days), To: now})
	if err != nil {
		return subTTLEstimate{}, err
	}
	// The loader returns whole sessions that touch the window; keep the window only, so a tripwire never
	// counts requests from before the change.
	from := now.AddDate(0, 0, -days).UnixMilli()
	in := reqs[:0]
	for _, r := range reqs {
		if r.TS >= from {
			in = append(in, r)
		}
	}
	return estimateSubTTL(analyze.SubagentTTL(in), onHour, d.Pricing, now, d.Fit, days), nil
}

// withdrawSubTTL takes Autopilot's own change back once the user withdrew the settings policy or turned
// Autopilot off or paused it: at a session end with no other session active, like any other change.
func withdrawSubTTL(ctx context.Context, dir string, d SettingsDeps, f config.AutopilotFile, now time.Time) (bool, error) {
	st, ok := f.Settings[SubTTLName]
	if !ok || st.State != config.AutopilotOn {
		return false, nil
	}
	own, err := fixer.FixInPlace(ctx, d.Fixer, st.FixID)
	if err != nil {
		return false, err // cannot tell: keep the record, try again at the next session end
	}
	if own {
		if busy, _ := fixer.OtherSessionActive(ctx, d.Fixer, d.SessionID); busy {
			return false, nil
		}
		if err := fixer.RevertSubagentTTL(ctx, d.Fixer, st.FixID, "1h"); err != nil {
			return false, err
		}
	}
	st.State, st.FixID, st.SinceMS, st.EvalMS = config.AutopilotOff, "", now.UnixMilli(), now.UnixMilli()
	st.Reason = "set back to the default because the settings policy is no longer agreed to or Autopilot is not running"
	f.Settings[SubTTLName] = st
	f.Record(config.AutopilotEvent{AtMS: now.UnixMilli(), Optimizer: SubTTLName, From: config.AutopilotOn, To: config.AutopilotOff, Reason: st.Reason})
	return true, config.SaveAutopilot(dir, f)
}

// EnableSettings records the user's agreement to the settings policy. It needs Autopilot on, and writes
// nothing outside ~/.trove: the setting is judged, and perhaps changed, only at a later session end.
func EnableSettings(dir string, now time.Time) (config.AutopilotFile, error) {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return f, err
	}
	if _, err := enableSettingsIn(&f, now); err != nil {
		return f, err
	}
	return f, config.SaveAutopilot(dir, f)
}

// enableSettingsIn records the agreement to the settings policy in f, which is not saved. The optimize policy's
// own yes covers it, so Enable calls this too.
func enableSettingsIn(f *config.AutopilotFile, now time.Time) (*config.AutopilotFile, error) {
	if !f.Enabled {
		return f, fmt.Errorf("Autopilot is not on. Run `trove autopilot on` first")
	}
	f.SettingsPolicy, f.SettingsConsentedAt = config.AutopilotSettingsPolicy, now.UnixMilli()
	if f.Settings == nil {
		f.Settings = map[string]config.AutopilotSetting{}
	}
	if st, ok := f.Settings[SubTTLName]; !ok || st.State == config.AutopilotOff && st.Attempts < subTTLMaxAttempts {
		f.Settings[SubTTLName] = config.AutopilotSetting{AutopilotEntry: config.AutopilotEntry{State: config.AutopilotShadow, SinceMS: now.UnixMilli(),
			Reason: "collecting estimates", Attempts: st.Attempts}}
	}
	return f, nil
}

// DisableSettings withdraws the agreement. Autopilot's own change, if any, is taken back at the next session
// end with no other session active (withdrawSubTTL); it returns that change's fix id so the user can undo it
// now with `trove undo <id>`.
func DisableSettings(dir string) (string, error) {
	f, err := config.LoadAutopilot(dir)
	if err != nil {
		return "", err
	}
	f.SettingsPolicy, f.SettingsConsentedAt = "", 0
	id := ""
	if st, ok := f.Settings[SubTTLName]; ok && st.State == config.AutopilotOn {
		id = st.FixID
	}
	return id, config.SaveAutopilot(dir, f)
}
