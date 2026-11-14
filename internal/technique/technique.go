// Package technique is the single list of every token-saving technique Trove has,
// could build, or could wrap, and the one place that decides whether a set of
// them can run together.
//
// Each entry records the facts that decide how a technique combines with the
// others: where it acts, how Trove gets it, whether it can change the cached
// prompt, which logins it works for, what it costs to carry (CLAUDE.md rule 15),
// and which scarce thing it claims, such as the agent's API endpoint. The list is
// the code form of docs/techniques.md, and a test keeps the two in step.
//
// Nothing here changes a setting or a request. Callers ask Check or CanAdd, and
// the answer is plain English they can show. Autopilot asks before it promotes
// an optimizer; `trove external` asks before it enables a wrapped tool.
package technique

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Layer is where a technique acts, in the order the layers act.
type Layer string

// The layers, earliest first.
const (
	LayerConfig       Layer = "config"        // settings and variables the agent reads at start
	LayerInstructions Layer = "instructions"  // rules text, skills, subagent definitions
	LayerToolBoundary Layer = "tool_boundary" // hooks and MCP tools, around a tool call
	LayerRequest      Layer = "request"       // the API request body, through the gateway
	LayerProvider     Layer = "provider"      // caching and billing, which Trove only observes
)

// Route is where the technique comes from.
type Route string

// The routes.
const (
	RouteNative Route = "native" // implemented in Go inside Trove
	RouteConfig Route = "config" // a documented setting of the agent, written with a diff and an undo
	RouteWrap   Route = "wrap"   // another project's tool, run through `trove external`
	RouteVendor Route = "vendor" // a small permissively licensed Go file copied in, with a notice
	RouteIdeas  Route = "ideas"  // read for ideas only; no code is taken
	RouteNone   Route = "none"   // not used
)

// Status says how far along a technique is.
type Status string

// The statuses.
const (
	StatusBuilt     Status = "built"     // works today
	StatusPartial   Status = "partial"   // part of it works; Note says which part
	StatusCandidate Status = "candidate" // planned or possible, not built
	StatusWrapped   Status = "wrapped"   // available through `trove external`
	StatusReference Status = "reference" // someone else's tool, studied for ideas only
	StatusExcluded  Status = "excluded"  // must not be used; Note says why
)

// Selectable reports whether a technique in this status can be turned on today.
func (s Status) Selectable() bool {
	return s == StatusBuilt || s == StatusPartial || s == StatusWrapped
}

// Login is how the user signs in to the agent. It decides whether the gateway
// can be used at all (CLAUDE.md rules 3 and 10).
type Login string

// The login types.
const (
	LoginAPIKey       Login = "api_key"
	LoginSubscription Login = "subscription"
	LoginUnknown      Login = "unknown"
)

// Resources a technique can claim. Two techniques that claim the same resource for
// different owners cannot both be on.
const (
	ResourceBaseURL         = "base_url"         // the agent's API endpoint (ANTHROPIC_BASE_URL)
	ResourceShellCompressor = "shell_compressor" // who shortens shell output and rewrites the command
	ResourceCodeNav         = "code_nav"         // who provides read and search tools
	ResourceBehaviorRules   = "behavior_rules"   // who asks the model for shorter replies
)

var resourceReason = map[string]string{
	ResourceBaseURL:         "both need to be the agent's API endpoint (ANTHROPIC_BASE_URL), so they cannot be chained",
	ResourceShellCompressor: "both shorten the same shell output and can rewrite the same command",
	ResourceCodeNav:         "both offer read and search tools with different output shapes",
	ResourceBehaviorRules:   "both add rules that ask for shorter replies, which stacks their cost and can make the rules contradict each other",
}

// Claim says a technique uses a resource, and who owns that use.
type Claim struct {
	Resource string `json:"resource"`
	Owner    string `json:"owner"`
}

// Capability is something a technique may need to exist before it can run.
const (
	CapMCPServer     = "mcp_server"     // Trove's MCP server is registered with the agent
	CapRecoveryStore = "recovery_store" // trimmed output can be stored and fetched back
	CapCustomAgent   = "custom_agent"   // an agent whose prompt layout Trove may change (CLAUDE.md of its own)
)

// Technique is one entry.
type Technique struct {
	// ID is stable and matches docs/techniques.md (B for built, C for candidate,
	// W for wrapped or studied tools).
	ID    string `json:"id"`
	Title string `json:"title"`
	// Names are the identifiers other code uses for it: optimizer names, fix
	// kinds, or `trove external` tool names. Empty for techniques with none yet.
	Names  []string `json:"names,omitempty"`
	Layers []Layer  `json:"layers"`
	Route  Route    `json:"route"`
	Status Status   `json:"status"`
	// Tier is lossless, recoverable or behavior (changes what the model sees or does).
	Tier string `json:"tier,omitempty"`
	// Prefix is true when the technique can change bytes already in the cached prompt.
	Prefix bool `json:"touches_cached_prompt"`
	// Subscription is true when it works for a Claude.ai login with no gateway.
	Subscription bool `json:"works_for_subscription"`
	// Gateway is true when it needs the gateway, which is for API keys only.
	Gateway bool `json:"needs_gateway"`
	// Auto is true when Autopilot may promote it (lossless, prefix-safe, built).
	Auto bool `json:"autopilot_eligible"`
	// AddsText is true when it puts text or tool definitions into prompts, which
	// rule 15 says must be declared and charged. Overhead is the declared tokens
	// per session when known; OverheadNote says where a measured figure comes from.
	AddsText     bool   `json:"adds_text"`
	Overhead     *int   `json:"overhead_tokens,omitempty"`
	OverheadNote string `json:"overhead_note,omitempty"`
	// UseOverhead is the tokens the technique adds to the context each time it acts (a denial
	// message, a hint, a marker), as opposed to Overhead, which every session that runs it pays.
	// Nil means not declared; 0 means it adds no text when it acts.
	UseOverhead *int `json:"overhead_per_use_tokens,omitempty"`
	// UseNetted is true when the technique's own saving estimate already subtracts UseOverhead, so
	// a reader of that estimate must not subtract it again. It is false when the estimate is the
	// tokens removed before the added text is counted.
	// NoSavingEstimate is true when the technique's shadow mode counts what it would have done but
	// cannot say how many tokens that would have saved (how much a flag or a hint removes depends on
	// the command). Such a technique has no evidence a saving threshold can be judged against.
	NoSavingEstimate bool     `json:"no_saving_estimate,omitempty"`
	UseNetted        bool     `json:"overhead_netted_in_estimate,omitempty"`
	Requires         []string `json:"requires,omitempty"`
	// HookNeeds lists the hook capabilities the technique relies on when it runs in the plugin's
	// hooks, as "Tool:kind" with kind input or output (for example "Bash:output"). They are required
	// only when it does not run through the gateway: a technique with a request layer is satisfied
	// by the gateway being in use, because the gateway needs no hook probe.
	HookNeeds []string `json:"hook_needs,omitempty"`
	Claims    []Claim  `json:"claims,omitempty"`
	// Conflicts lists IDs that cannot be on together with this one, beyond what
	// claims already cover.
	Conflicts []string `json:"conflicts,omitempty"`
	Licence   string   `json:"licence"`
	Source    string   `json:"source,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// Has reports whether the technique goes by name (an ID or one of its Names).
func (t Technique) Has(name string) bool {
	if strings.EqualFold(t.ID, name) {
		return true
	}
	for _, n := range t.Names {
		if n == name {
			return true
		}
	}
	return false
}

// All returns a copy of the registry: built techniques first, then candidates, then
// other people's tools, each group in numeric order of its ID.
func All() []Technique {
	out := make([]Technique, len(registry))
	copy(out, registry)
	sort.SliceStable(out, func(i, j int) bool { return idLess(out[i].ID, out[j].ID) })
	return out
}

// idLess orders IDs by letter (B, C, W) and then by number, so B15 follows B14.
func idLess(a, b string) bool {
	if a[0] != b[0] {
		return a[0] < b[0]
	}
	na, _ := strconv.Atoi(a[1:])
	nb, _ := strconv.Atoi(b[1:])
	return na < nb
}

// Get finds a technique by ID or name.
func Get(name string) (Technique, bool) {
	for _, t := range registry {
		if t.Has(name) {
			return t, true
		}
	}
	return Technique{}, false
}

// Context is what Check needs to know about the user's setup.
type Context struct {
	Login Login
	// GatewayOn is true when the gateway is set up for this user.
	GatewayOn bool
	// Have lists the capabilities that exist (CapMCPServer, ...).
	Have []string
	// HookCaps lists the hook rewrites a probe has seen work for a probed agent, as "Tool:kind",
	// in the same form as Technique.HookNeeds.
	HookCaps []string
}

func (c Context) has(cap string) bool {
	for _, h := range c.Have {
		if h == cap {
			return true
		}
	}
	return false
}

// Conflict is two selected techniques that cannot run together.
type Conflict struct {
	A      string `json:"a"`
	B      string `json:"b"`
	Reason string `json:"reason"`
}

// Unmet is a selected technique that cannot run in this setup.
type Unmet struct {
	ID      string `json:"id"`
	Problem string `json:"problem"`
}

// Result is the outcome of a check.
type Result struct {
	Conflicts []Conflict `json:"conflicts,omitempty"`
	Unmet     []Unmet    `json:"unmet,omitempty"`
	// Unknown lists names that are not in the registry.
	Unknown []string `json:"unknown,omitempty"`
}

// OK is true when nothing is wrong.
func (r Result) OK() bool { return len(r.Conflicts) == 0 && len(r.Unmet) == 0 && len(r.Unknown) == 0 }

// Reasons returns every problem as one plain-English line, in a stable order.
func (r Result) Reasons() []string {
	var out []string
	for _, u := range r.Unknown {
		out = append(out, fmt.Sprintf("%q is not a technique Trove knows about.", u))
	}
	for _, u := range r.Unmet {
		out = append(out, fmt.Sprintf("%s: %s", u.ID, u.Problem))
	}
	for _, c := range r.Conflicts {
		out = append(out, c.Reason)
	}
	return out
}

// Check says whether the named techniques can all be on in this setup. It is a
// pure function of its inputs, and its output is stable, so it can be tested and
// shown to the user as it is.
func Check(names []string, ctx Context) Result {
	var res Result
	var sel []Technique
	seen := map[string]bool{}
	for _, n := range names {
		t, ok := Get(n)
		if !ok {
			res.Unknown = append(res.Unknown, n)
			continue
		}
		if seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		sel = append(sel, t)
	}
	sort.SliceStable(sel, func(i, j int) bool { return sel[i].ID < sel[j].ID })

	for _, t := range sel {
		for _, p := range t.unmet(ctx) {
			res.Unmet = append(res.Unmet, Unmet{t.ID, p})
		}
	}
	for i := 0; i < len(sel); i++ {
		for j := i + 1; j < len(sel); j++ {
			if c, ok := conflict(sel[i], sel[j]); ok {
				res.Conflicts = append(res.Conflicts, c)
			}
		}
	}
	sort.Strings(res.Unknown)
	return res
}

// CanAdd checks the current set plus one more, and keeps only the problems that
// involve the one being added, so a caller can refuse just that step.
func CanAdd(current []string, add string, ctx Context) Result {
	all := Check(append(append([]string{}, current...), add), ctx)
	t, ok := Get(add)
	if !ok {
		return Result{Unknown: []string{add}}
	}
	var out Result
	for _, u := range all.Unmet {
		if u.ID == t.ID {
			out.Unmet = append(out.Unmet, u)
		}
	}
	for _, c := range all.Conflicts {
		if c.A == t.ID || c.B == t.ID {
			out.Conflicts = append(out.Conflicts, c)
		}
	}
	return out
}

// unmet lists why the technique cannot run in this setup.
func (t Technique) unmet(ctx Context) []string {
	var out []string
	switch t.Status {
	case StatusCandidate:
		out = append(out, "it is not built yet")
	case StatusReference:
		out = append(out, "it is studied for ideas only and is not available to turn on")
	case StatusExcluded:
		out = append(out, "it is not allowed: "+t.Note)
	}
	if t.Gateway {
		switch {
		case ctx.Login == LoginSubscription:
			out = append(out, "it needs the gateway, which works only with an API key or API credits; Trove never touches a subscription login")
		case ctx.Login == LoginUnknown:
			out = append(out, "it needs the gateway, which works only with an API key or API credits, and Trove cannot tell which login you use")
		case !ctx.GatewayOn:
			out = append(out, "it needs the gateway; set it up with `trove setup --gateway`")
		}
	} else if !t.Subscription && ctx.Login == LoginSubscription {
		out = append(out, "it does not work with a subscription login")
	}
	for _, r := range t.Requires {
		if !ctx.has(r) {
			out = append(out, "it needs "+capabilityText(r))
		}
	}
	if len(t.HookNeeds) > 0 && !(t.hasLayer(LayerRequest) && ctx.GatewayOn) {
		for _, n := range t.HookNeeds {
			if !ctx.hasHookCap(n) {
				out = append(out, "it needs a hook probe that showed "+hookNeedText(n)+" works for your agent (run /trove:setup, or `trove hooks verify`)")
			}
		}
	}
	return out
}

func capabilityText(c string) string {
	switch c {
	case CapMCPServer:
		return "Trove's MCP server registered with your agent (`trove mcp install`)"
	case CapRecoveryStore:
		return "the local recovery store, so trimmed output can be fetched back"
	case CapCustomAgent:
		return "a custom agent whose prompt Trove may lay out; Claude Code and Codex manage their own"
	}
	return c
}

// conflict says whether two techniques cannot be on together, and why.
func conflict(a, b Technique) (Conflict, bool) {
	mk := func(reason string) (Conflict, bool) { return Conflict{A: a.ID, B: b.ID, Reason: reason}, true }
	if contains(a.Conflicts, b.ID) || contains(b.Conflicts, a.ID) {
		return mk(fmt.Sprintf("%s (%s) cannot run together with %s (%s).", a.ID, a.label(), b.ID, b.label()))
	}
	for _, ca := range a.Claims {
		for _, cb := range b.Claims {
			if ca.Resource == cb.Resource && ca.Owner != cb.Owner {
				return mk(fmt.Sprintf("%s (%s) and %s (%s): %s.", a.ID, a.label(), b.ID, b.label(), resourceReason[ca.Resource]))
			}
		}
	}
	return Conflict{}, false
}

func (t Technique) label() string {
	if len(t.Names) > 0 {
		return t.Names[0]
	}
	return t.Title
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// OverlappingBuiltins returns the names of built-in optimizers that conflict with
// the technique called name, so a caller that thinks in optimizer names can ask
// the registry instead of keeping its own list.
func OverlappingBuiltins(name string) []string {
	t, ok := Get(name)
	if !ok {
		return nil
	}
	var out []string
	for _, o := range registry {
		if o.ID == t.ID || o.Status != StatusBuilt && o.Status != StatusPartial {
			continue
		}
		if _, bad := conflict(t, o); bad {
			out = append(out, o.Names...)
		}
	}
	sort.Strings(out)
	return out
}

func (t Technique) hasLayer(l Layer) bool {
	for _, x := range t.Layers {
		if x == l {
			return true
		}
	}
	return false
}

func (c Context) hasHookCap(n string) bool {
	for _, h := range c.HookCaps {
		if h == n {
			return true
		}
	}
	return false
}

// hookNeedText turns "Bash:output" into "Bash output rewriting".
func hookNeedText(n string) string {
	tool, kind, _ := strings.Cut(n, ":")
	return tool + " " + kind + " rewriting"
}

// ValidHookNeed reports whether n is a well-formed "Tool:kind" hook capability.
func ValidHookNeed(n string) bool {
	tool, kind, ok := strings.Cut(n, ":")
	return ok && tool != "" && (kind == "input" || kind == "output")
}

// autoProblem says why a technique may not be marked for Autopilot, or "" when it may. Autopilot promotes
// only what is built, lossless and leaves the cached prompt alone. Text it adds is allowed only when the
// cost is declared as numbers (per session and per use), so the promotion decision can charge for it.
func autoProblem(x Technique) string {
	if x.Status != StatusBuilt || x.Tier != tierLossless || x.Prefix {
		return "it is not a built, lossless, prefix-safe technique"
	}
	if x.AddsText && (x.Overhead == nil || x.UseOverhead == nil) {
		return "it adds text but does not declare both its per-session and per-use overhead as numbers"
	}
	return ""
}

// Overhead is what a technique costs the context, for netting against a saving estimate.
type Overhead struct {
	PerSession int64 // tokens every session that runs it pays
	PerUse     int64 // tokens each time it acts
	Netted     bool  // its own estimate already subtracts PerUse
}

// OverheadOf returns the declared overhead of the named technique. ok is false when the name is unknown
// or the technique adds text without declaring numbers, in which case the caller must not promote it.
func OverheadOf(name string) (o Overhead, ok bool) {
	t, found := Get(name)
	if !found {
		return Overhead{}, false
	}
	if t.AddsText && (t.Overhead == nil || t.UseOverhead == nil) {
		return Overhead{}, false
	}
	if t.Overhead != nil {
		o.PerSession = int64(*t.Overhead)
	}
	if t.UseOverhead != nil {
		o.PerUse = int64(*t.UseOverhead)
	}
	o.Netted = t.UseNetted
	return o, true
}
