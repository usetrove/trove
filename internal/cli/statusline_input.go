package cli

import (
	"encoding/json"
	"io"
	"math"
	"os"

	"time"

	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/report"
	"github.com/usetrove/trove/internal/store"
)

// claudeInput is the part of the JSON Claude Code sends a status line command on stdin that Trove uses.
// Fields verified on https://code.claude.com/docs/en/statusline on 2026-11-23:
//
//	session_id                                   string
//	rate_limits.five_hour.used_percentage        number, 0-100 (Pro, Max and Ultra only, after the first response)
//	rate_limits.five_hour.resets_at              Unix epoch seconds
//	rate_limits.seven_day.used_percentage        number, 0-100 (same conditions; each window may be absent)
//	rate_limits.seven_day.resets_at              Unix epoch seconds
//	prompt_cache.warm                            bool (v2.1.251+, main conversation, after its first response)
//	prompt_cache.ttl                             "5m" or "1h"
//	model.id                                     string
//	version                                      string
//
// Nothing else is decoded: the folders, transcript path, session name and the rest are never read, so
// they cannot reach the database (PRINCIPLES.md rule 7). A missing, mistyped or out-of-range value is
// treated as absent (rule 9), and a problem with one field never loses another.
type claudeInput struct {
	SessionID       string
	FiveHour        *report.LimitWindow
	SevenDay        *report.LimitWindow
	CacheWarm       *bool
	CacheTTLSeconds *int64
	Model           string
	Version         string
}

// hasMeter reports whether the input carries anything worth recording.
func (in claudeInput) hasMeter() bool {
	return in.FiveHour != nil || in.SevenDay != nil || in.CacheWarm != nil || in.CacheTTLSeconds != nil
}

// planUsage is the meter for display and --json, or nil when Claude Code sent no window.
func (in claudeInput) planUsage() *report.PlanUsage {
	if in.FiveHour == nil && in.SevenDay == nil {
		return nil
	}
	return &report.PlanUsage{Source: report.PlanUsageSource, FiveHour: in.FiveHour, SevenDay: in.SevenDay}
}

// readClaudeInput reads Claude Code's status line JSON. It reads only for the claude format and only when
// stdin is a pipe or file, never a terminal, so running the command by hand does not wait for input.
func readClaudeInput(a *App, format string) claudeInput {
	if format != report.FormatClaude || a.In == nil {
		return claudeInput{}
	}
	if f, ok := a.In.(*os.File); ok {
		if fi, err := f.Stat(); err != nil || fi.Mode()&os.ModeCharDevice != 0 {
			return claudeInput{}
		}
	}
	var top map[string]json.RawMessage
	if json.NewDecoder(io.LimitReader(a.In, 1<<20)).Decode(&top) != nil {
		return claudeInput{}
	}
	return parseClaudeInput(top)
}

func parseClaudeInput(top map[string]json.RawMessage) claudeInput {
	var in claudeInput
	_ = json.Unmarshal(top["session_id"], &in.SessionID)

	var limits map[string]json.RawMessage
	if json.Unmarshal(top["rate_limits"], &limits) == nil {
		in.FiveHour = parseWindow(limits["five_hour"])
		in.SevenDay = parseWindow(limits["seven_day"])
	}

	var cache struct {
		Warm *bool   `json:"warm"`
		TTL  *string `json:"ttl"`
	}
	if json.Unmarshal(top["prompt_cache"], &cache) == nil {
		in.CacheWarm = cache.Warm
		if cache.TTL != nil {
			var s int64
			switch *cache.TTL {
			case "5m":
				s = 300
			case "1h":
				s = 3600
			}
			if s > 0 {
				in.CacheTTLSeconds = &s
			}
		}
	}

	var model struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(top["model"], &model) == nil && store.ValidModelID(model.ID) {
		in.Model = model.ID
	}
	var version string
	if json.Unmarshal(top["version"], &version) == nil && store.ValidVersion(version) {
		in.Version = version
	}
	return in
}

// maxResetSeconds rejects reset times that cannot be real (after the year 2200).
const maxResetSeconds = 7258118400

func parseWindow(raw json.RawMessage) *report.LimitWindow {
	var w struct {
		UsedPercentage *float64 `json:"used_percentage"`
		ResetsAt       *float64 `json:"resets_at"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &w) != nil || w.UsedPercentage == nil {
		return nil
	}
	p := *w.UsedPercentage
	if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 100 {
		return nil
	}
	out := &report.LimitWindow{UsedPercent: p, Label: measure.Measured}
	if r := w.ResetsAt; r != nil && *r > 0 && *r < maxResetSeconds {
		at := time.UnixMilli(int64(*r * 1000)).UTC()
		out.ResetsAt = &at
	}
	return out
}
