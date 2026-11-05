package optimize

import (
	"fmt"
	"strings"
)

// The capability matrix says, for every optimizer and every adapter, whether the
// adapter can run it: supported, partial or impossible, with the reason. It is
// computed from what optimizers declare (Points, Tier, OverheadTokens, and the
// optional SupportOverrider and Caveater) and from what each adapter can reach
// (abilities below). Nothing in it is written by hand per optimizer, so a new
// optimizer appears in `trove doctor --capabilities` and docs/adapters.md as
// soon as it is registered.

// VerifiedOn is when the harness facts in abilities were last checked against
// official documentation (CLAUDE.md rule 8).
const VerifiedOn = "2026-11-04"

// Caveater is implemented by optimizers whose support comes with a condition the
// matrix should state, for example "custom agents only".
type Caveater interface {
	Caveat() string
}

// abilities says what each adapter can do at each point. The reasons are facts
// about the harness or the gateway, not about any one optimizer.
var abilities = map[Adapter]map[Point]Support{
	AdapterGateway: {
		PointRequest:    {LevelSupported, "sees and can rewrite every outgoing API request"},
		PointToolOutput: {LevelSupported, "tool results are part of each request's history"},
		PointToolInput:  {LevelImpossible, "sees a tool call only after it ran, as history in a later request"},
	},
	AdapterHooks: {
		PointToolInput:  {LevelSupported, "PreToolUse can deny a call, add context or rewrite its input (code.claude.com/docs/en/hooks, " + VerifiedOn + ")"},
		PointToolOutput: {LevelPartial, "PostToolUse documents updatedToolOutput, but the value must match each tool's output shape and built-in tools silently ignore a mismatch, so the adapter emits it only for tools a live probe has seen honour it (`trove hooks probe`); whether later requests keep the replaced output is unanswered (docs/non-gateway-optimization.md, Q2)"},
		PointRequest:    {LevelImpossible, "no hook receives the model request (none documented as of " + VerifiedOn + ")"},
	},
	AdapterPlugin: {
		PointToolInput:  {LevelSupported, "reachable over `trove engine serve`; what a harness lets a plugin change is checked per harness"},
		PointToolOutput: {LevelSupported, "reachable over `trove engine serve`; what a harness lets a plugin change is checked per harness"},
		PointRequest:    {LevelPartial, "only in harnesses that expose the outgoing request to plugins"},
	},
}

// Row is one optimizer's line in the matrix.
type Row struct {
	Optimizer      string              `json:"optimizer"`
	Tier           Tier                `json:"tier"`
	Points         []Point             `json:"points"`
	OverheadTokens int                 `json:"overhead_tokens"`
	Support        map[Adapter]Support `json:"support"`
}

// Matrix returns one row per registered optimizer: the request pipeline first,
// in its fixed order, then the tool-level optimizers.
func Matrix() []Row {
	var all []Capable
	seen := map[string]bool{}
	add := func(b any) {
		if c, ok := b.(Capable); ok && !seen[c.Name()] {
			seen[c.Name()] = true
			all = append(all, c)
		}
	}
	for _, o := range Pipeline() {
		add(o)
	}
	for _, o := range ToolPipeline() {
		add(o)
	}
	rows := make([]Row, 0, len(all))
	for _, c := range all {
		r := Row{Optimizer: c.Name(), Tier: c.Tier(), Points: c.Points(), OverheadTokens: c.OverheadTokens(), Support: map[Adapter]Support{}}
		for _, a := range Adapters {
			r.Support[a] = supportFor(c, a)
		}
		rows = append(rows, r)
	}
	return rows
}

func rankLevel(l Level) int {
	switch l {
	case LevelSupported:
		return 2
	case LevelPartial:
		return 1
	}
	return 0
}

// supportFor combines an optimizer's points with an adapter's abilities: the best
// point the adapter can reach decides the level. An optimizer's own override wins.
func supportFor(c Capable, a Adapter) Support {
	if ov, ok := c.(SupportOverrider); ok {
		if s, ok := ov.Support(a); ok {
			return s
		}
	}
	best := Support{Level: LevelImpossible, Reason: "the adapter cannot reach any point this optimizer acts at"}
	for _, p := range c.Points() {
		s, ok := abilities[a][p]
		if ok && rankLevel(s.Level) > rankLevel(best.Level) {
			best = Support{Level: s.Level, Reason: string(p) + ": " + s.Reason}
		}
	}
	if best.Level == LevelImpossible {
		pts := make([]string, 0, len(c.Points()))
		for _, p := range c.Points() {
			pts = append(pts, string(p)+" ("+abilities[a][p].Reason+")")
		}
		best.Reason = strings.Join(pts, "; ")
		return best
	}
	if cv, ok := c.(Caveater); ok && cv.Caveat() != "" {
		best.Reason += "; " + cv.Caveat()
	}
	return best
}

// Reaches reports whether the adapter can run the optimizer at all (supported
// or partial). The parity test runs every optimizer both adapters reach.
func (r Row) Reaches(a Adapter) bool { return r.Support[a].Level != LevelImpossible }

// RenderMarkdown renders the matrix as the markdown table in docs/adapters.md.
func RenderMarkdown(rows []Row) string {
	var sb strings.Builder
	sb.WriteString("| Optimizer | Tier | Acts at | Overhead (tokens) |")
	for _, a := range Adapters {
		fmt.Fprintf(&sb, " %s |", a)
	}
	sb.WriteString("\n|---|---|---|---|")
	for range Adapters {
		sb.WriteString("---|")
	}
	sb.WriteString("\n")
	for _, r := range rows {
		fmt.Fprintf(&sb, "| `%s` | %s | %s | %d |", r.Optimizer, r.Tier, joinPoints(r.Points), r.OverheadTokens)
		for _, a := range Adapters {
			fmt.Fprintf(&sb, " **%s**: %s |", r.Support[a].Level, escapePipes(r.Support[a].Reason))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// RenderText renders the matrix for a terminal.
func RenderText(rows []Row) string {
	var sb strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&sb, "%s  (%s; acts at %s; overhead %d tokens)\n", r.Optimizer, r.Tier, joinPoints(r.Points), r.OverheadTokens)
		for _, a := range Adapters {
			fmt.Fprintf(&sb, "  %-8s %-10s %s\n", a, r.Support[a].Level, r.Support[a].Reason)
		}
	}
	return sb.String()
}

func joinPoints(ps []Point) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = string(p)
	}
	return strings.Join(s, ", ")
}

func escapePipes(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

// Caveat implements Caveater.
func (VolatilePrefixMover) Caveat() string {
	return "only for agents the user marked as custom, never Claude Code or Codex"
}

// Caveat implements Caveater.
func (CacheBreakpoints) Caveat() string {
	return "only for agents the user marked as custom, and only for known models above the cache minimum"
}

// Caveat implements Caveater.
func (OutputTrimmer) Caveat() string {
	return "stays in shadow unless the full output can be fetched back (recovery store and the Trove MCP server)"
}
