package fixer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// model_default: your Claude Code history shows a frontier model answering many
// small tasks (a small prompt and a small reply: the analyzer's
// expensive_model_small_task finding), so a cheaper default may do the same work.
// The proposal sets "model" in ~/.claude/settings.json (verified: user settings
// carry the default model, code.claude.com/docs/en/settings).
//
// This is a suggestion, never part of --all, because Trove cannot check that the
// cheaper model handles your tasks as well. It is skipped when the model is
// forced by something Trove must not override: ANTHROPIC_MODEL in the
// environment (it wins over the file), a managed policy, or a project setting.
//
// Codex is not covered: its history format is unverified.
const (
	modelMinRequests = 20
	modelMinShare    = 0.4 // share of the frontier model's requests that were small tasks
)

func detectModelDefault(ctx context.Context, e *Env) ([]*Fix, error) {
	if e.DB == nil || e.ClaudeDir == "" {
		return nil, nil
	}
	since := e.now().Add(-time.Duration(e.Cfg.LookbackDays) * 24 * time.Hour).UnixMilli()
	type agg struct {
		total, small int
		alts         map[string]int
	}
	by := map[string]*agg{}
	get := func(m string) *agg {
		if by[m] == nil {
			by[m] = &agg{alts: map[string]int{}}
		}
		return by[m]
	}
	// Only Claude Code requests: gateway rows from the claude_code agent, and
	// history rows from the Claude Code source. A session appears in one or the
	// other, so the two are not double counted.
	for _, q := range []struct{ count, finds string }{
		{`SELECT model, COUNT(*) FROM requests WHERE ts >= ? AND agent = 'claude_code' AND model != '' GROUP BY model`,
			`SELECT model, findings FROM requests WHERE ts >= ? AND agent = 'claude_code' AND findings LIKE '%expensive_model_small_task%'`},
		{`SELECT model, COUNT(*) FROM history_requests WHERE ts >= ? AND source = 'claude_code' AND sidechain = 0 AND model != '' GROUP BY model`,
			`SELECT model, findings FROM history_requests WHERE ts >= ? AND source = 'claude_code' AND sidechain = 0 AND findings LIKE '%expensive_model_small_task%'`},
	} {
		rows, err := e.DB.QueryContext(ctx, q.count, since)
		if err != nil {
			continue // a table that is not there yet means no data from it
		}
		for rows.Next() {
			var m string
			var n int
			if rows.Scan(&m, &n) == nil {
				get(m).total += n
			}
		}
		_ = rows.Close()
		rows, err = e.DB.QueryContext(ctx, q.finds, since)
		if err != nil {
			continue
		}
		for rows.Next() {
			var m, raw string
			if rows.Scan(&m, &raw) != nil {
				continue
			}
			var fs []struct {
				Detector string         `json:"detector"`
				Detail   map[string]any `json:"detail"`
			}
			if jsonUnmarshal(raw, &fs) != nil {
				continue
			}
			for _, f := range fs {
				if f.Detector == "expensive_model_small_task" {
					a := get(m)
					a.small++
					if alt, _ := f.Detail["alternative"].(string); alt != "" {
						a.alts[alt]++
					}
				}
			}
		}
		_ = rows.Close()
	}

	models := make([]string, 0, len(by))
	for m := range by {
		models = append(models, m)
	}
	sort.Strings(models)
	var out []*Fix
	for _, m := range models {
		a := by[m]
		if a.small < modelMinRequests || a.total == 0 || float64(a.small) < modelMinShare*float64(a.total) {
			continue
		}
		alt, best := "", 0
		for k, n := range a.alts {
			if n > best || (n == best && k < alt) {
				alt, best = k, n
			}
		}
		if alt == "" {
			continue
		}
		settings := filepath.Join(e.ClaudeDir, "settings.json")
		old, err := os.ReadFile(settings) //nolint:gosec // an allowed agent config file
		if err != nil {
			old = nil
		}
		f := &Fix{
			ID: fixID(ModelDefault, settings, m, alt), Kind: ModelDefault, Agent: "claude_code", Path: settings, Suggestion: true,
			Title: fmt.Sprintf("Make %s your default model instead of %s", alt, m),
			Why: fmt.Sprintf("%d of your %d recent %s requests (%.0f%%) were small tasks: a short prompt and a short reply. "+
				"A cheaper default model often handles those, and you can still pick %s for hard work with /model.", a.small, a.total, m, 100*float64(a.small)/float64(a.total), m),
			Evidence: []string{
				fmt.Sprintf("%d of %d requests on %s in the last %d days were small tasks", a.small, a.total, m, e.Cfg.LookbackDays),
			},
			Notes: []string{
				"This is a suggestion. Trove cannot check that " + alt + " does as well on your tasks, so try it on a few before keeping it.",
				"It changes the default for every project. Undo with `trove undo`.",
				"Switching models mid-session starts a fresh prompt cache; the change applies to new sessions.",
				"No saving is estimated: Trove cannot know how many tasks would move to the cheaper model.",
			},
			old: old,
		}
		switch {
		case e.getenv("ANTHROPIC_MODEL") != "":
			f.Advisory = "The ANTHROPIC_MODEL environment variable is set, and it overrides the model in settings files. Unset it, or change it where you set it."
		default:
			if why := modelSetElsewhere(e); why != "" {
				f.Advisory = why
			} else if upd, err := SetJSONString(old, "model", alt); err != nil {
				f.Advisory = fmt.Sprintf("%s cannot be edited safely (%v). Set \"model\": %q there yourself.", settings, err, alt)
			} else {
				f.updated = upd
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// modelSetElsewhere says why editing the user setting would not take effect: a
// project setting that sets "model" wins over it. (A managed model wins over
// everything; Trove never reads managed files, so it cannot see one. The
// advice in the note covers it.)
func modelSetElsewhere(e *Env) string {
	if e.ProjectDir == "" {
		return ""
	}
	for _, p := range []string{filepath.Join(e.ProjectDir, ".claude", "settings.local.json"), filepath.Join(e.ProjectDir, ".claude", "settings.json")} {
		b, err := os.ReadFile(p) //nolint:gosec // an allowed agent config file
		if err != nil {
			continue
		}
		doc := map[string]json.RawMessage{}
		if json.Unmarshal(b, &doc) == nil {
			if _, ok := doc["model"]; ok {
				return "This project's " + strings.TrimPrefix(filepath.ToSlash(p), filepath.ToSlash(e.ProjectDir)+"/") + " sets its own model, which wins over your default. Change it there if you want."
			}
		}
	}
	return ""
}
