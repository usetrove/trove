package recommend

import (
	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/features"
)

// Aggregate summarizes a repo's recent sessions: counts, and per-session means.
// Numbers only.
type Aggregate struct {
	Sessions     int     `json:"sessions"`
	FilesRead    float64 `json:"files_read"`   // mean per session
	FilesEdited  float64 `json:"files_edited"` // mean per session
	RepeatReads  float64 `json:"repeat_reads"`
	ScoutCalls   float64 `json:"scout_calls"`
	Compactions  float64 `json:"compactions"`
	Retries      float64 `json:"retries"`
	ToolResults  float64 `json:"tool_results"`
	LargeResults float64 `json:"large_results"`
	DupRatio     float64 `json:"dup_ratio"`   // mean of the sessions' average duplicate-line ratios
	ErrorShare   float64 `json:"error_share"` // mean
	StackShare   float64 `json:"stack_share"` // mean
}

// Summarize averages sessions.
func Summarize(ss []features.SessionFeatures) Aggregate {
	a := Aggregate{Sessions: len(ss)}
	if len(ss) == 0 {
		return a
	}
	for _, s := range ss {
		a.FilesRead += float64(s.FilesRead)
		a.FilesEdited += float64(s.FilesEdited)
		a.RepeatReads += float64(s.RepeatReads)
		a.ScoutCalls += float64(s.ScoutCalls)
		a.Compactions += float64(s.Compactions)
		a.Retries += float64(s.Retries)
		a.ToolResults += float64(s.ToolResults)
		a.LargeResults += float64(s.LargeResults)
		a.DupRatio += s.AvgDupRatio
		a.ErrorShare += s.ErrorShare
		a.StackShare += s.StackShare
	}
	n := float64(len(ss))
	for _, p := range []*float64{&a.FilesRead, &a.FilesEdited, &a.RepeatReads, &a.ScoutCalls, &a.Compactions, &a.Retries,
		&a.ToolResults, &a.LargeResults, &a.DupRatio, &a.ErrorShare, &a.StackShare} {
		*p /= n
	}
	return a
}

// Facts is everything a Provider may use. It holds no content: features,
// experiment results and optimizer states only.
type Facts struct {
	// Repo is the repo's bucketed features; nil when not computed yet.
	Repo *features.Repo
	// Sessions summarizes the repo's recent sessions.
	Sessions Aggregate
	// Experiments are the user's own A/B results (Prompt 18's internal/experiment).
	Experiments []experiment.Result
	// States maps optimizer name to its current state: off, shadow or on.
	States map[string]string
	// RolledBack lists optimizers a guardrail rolled back on this device.
	RolledBack []string
	// Task is the task type (quick-fix, feature, refactor, exploration), or "".
	Task string
}

func (f Facts) state(name string) string {
	if s, ok := f.States[name]; ok && s != "" {
		return s
	}
	return "off"
}

func (f Facts) rolledBack(name string) bool {
	for _, n := range f.RolledBack {
		if n == name {
			return true
		}
	}
	return false
}
