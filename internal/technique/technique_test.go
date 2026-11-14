package technique

import (
	"reflect"
	"strings"
	"testing"
)

func TestRegistryInvariants(t *testing.T) {
	ids, names := map[string]bool{}, map[string]string{}
	validLayer := map[Layer]bool{LayerConfig: true, LayerInstructions: true, LayerToolBoundary: true, LayerRequest: true, LayerProvider: true}
	validRoute := map[Route]bool{RouteNative: true, RouteConfig: true, RouteWrap: true, RouteVendor: true, RouteIdeas: true, RouteNone: true}
	validStatus := map[Status]bool{StatusBuilt: true, StatusPartial: true, StatusCandidate: true, StatusWrapped: true, StatusReference: true, StatusExcluded: true}
	for _, x := range registry {
		id := x.ID
		if id == "" || ids[id] {
			t.Errorf("%q: empty or duplicate ID", id)
		}
		ids[id] = true
		if x.Title == "" || x.Licence == "" {
			t.Errorf("%s: needs a title and a licence", id)
		}
		if len(x.Layers) == 0 {
			t.Errorf("%s: no layer", id)
		}
		for _, l := range x.Layers {
			if !validLayer[l] {
				t.Errorf("%s: unknown layer %q", id, l)
			}
		}
		if !validRoute[x.Route] || !validStatus[x.Status] {
			t.Errorf("%s: unknown route %q or status %q", id, x.Route, x.Status)
		}
		for _, n := range x.Names {
			if other, dup := names[n]; dup {
				t.Errorf("%s: name %q is already used by %s", id, n, other)
			}
			names[n] = id
		}
		// A technique that needs the gateway cannot work with no gateway, so it is not a subscription technique.
		if x.Gateway && x.Subscription {
			t.Errorf("%s: needs the gateway and also claims to work for a subscription login", id)
		}
		// Rule 15: anything that puts text or tool definitions into prompts says what it costs, or where that is measured.
		if x.AddsText && x.Overhead == nil && x.OverheadNote == "" {
			t.Errorf("%s: adds text to prompts but declares no overhead and no place it is measured (rule 15)", id)
		}
		// The overhead of a technique that is on must be known or have a source.
		if x.Status.Selectable() && x.Overhead == nil && x.OverheadNote == "" {
			t.Errorf("%s: can be turned on but has no declared overhead (rule 15)", id)
		}
		// Autopilot may only promote what is built, lossless, leaves the cached prompt alone, and charges any text it adds.
		if x.Auto {
			if p := autoProblem(x); p != "" {
				t.Errorf("%s: marked for Autopilot but %s", id, p)
			}
		}
		// A technique that can change the cached prompt is never automatic.
		if x.Prefix && x.Auto {
			t.Errorf("%s: touches the cached prompt and is automatic", id)
		}
		// Licence rules (docs/techniques.md): only MIT or Apache-2.0 code may be vendored, and a source-available
		// licence must be named so a reader can see why the build is pinned.
		if x.Route == RouteVendor && !strings.HasPrefix(x.Licence, "MIT") && !strings.HasPrefix(x.Licence, "Apache-2.0") {
			t.Errorf("%s: vendored code must be MIT or Apache-2.0, got %q", id, x.Licence)
		}
		if x.Status == StatusExcluded && x.Note == "" {
			t.Errorf("%s: excluded without a reason", id)
		}
		if x.Status == StatusExcluded && x.Route != RouteNone {
			t.Errorf("%s: excluded but its route is %q", id, x.Route)
		}
		if (x.Status == StatusReference) != (x.Route == RouteIdeas) {
			t.Errorf("%s: reference status and ideas route go together", id)
		}
		if x.Route == RouteWrap && x.Source == "" && x.Licence != "Trove project" {
			t.Errorf("%s: a wrapped tool needs its source", id)
		}
		if strings.Contains(x.Licence, "BUSL") && x.Route == RouteVendor {
			t.Errorf("%s: BUSL code cannot be vendored", id)
		}
	}
	for _, x := range registry {
		for _, c := range x.Conflicts {
			if !ids[c] || c == x.ID {
				t.Errorf("%s: conflicts with %q, which is not another entry", x.ID, c)
			}
		}
		for _, c := range x.Claims {
			if _, ok := resourceReason[c.Resource]; !ok || c.Owner == "" {
				t.Errorf("%s: claim %+v has no reason text or no owner", x.ID, c)
			}
		}
		for _, r := range x.Requires {
			if capabilityText(r) == r {
				t.Errorf("%s: requires %q, which has no plain-English text", x.ID, r)
			}
		}
	}
}

func hasConflict(r Result, a, b string) bool {
	for _, c := range r.Conflicts {
		if (c.A == a && c.B == b) || (c.A == b && c.B == a) {
			return true
		}
	}
	return false
}

func TestTrovesOwnTechniquesWorkTogether(t *testing.T) {
	ctx := Context{Login: LoginAPIKey, GatewayOn: true, Have: []string{CapMCPServer, CapRecoveryStore, CapCustomAgent},
		HookCaps: []string{"Bash:output", "Bash:input", "Read:input"}}
	// Everything built that is meant to run in the same pipeline, by name and by ID.
	r := Check([]string{"noise_normalizer", "output_trimmer", "duplicate_result_collapser", "hook_large_output", "hook_repeat_read",
		"hook_read_limit", "hook_quiet_commands", "trove_mcp", "volatile_prefix_mover", "cache_breakpoints", "B12", "B14"}, ctx)
	if !r.OK() {
		t.Fatalf("Trove's own built techniques are meant to be combined: %v", r.Reasons())
	}
}

func TestWrappedShellCompressorConflictsWithTheBuiltInOutputOptimizers(t *testing.T) {
	ctx := Context{Login: LoginAPIKey, GatewayOn: true, Have: []string{CapMCPServer, CapRecoveryStore}}
	r := Check([]string{"rtk", "noise_normalizer", "output_trimmer", "hook_large_output", "hook_quiet_commands", "hook_repeat_read"}, ctx)
	for _, b := range []string{"B1", "B3", "B8"} {
		if !hasConflict(r, "W1", b) {
			t.Errorf("RTK should conflict with %s: %v", b, r.Reasons())
		}
	}
	// This is how `trove external` behaves today; changing it is a decision, not a side effect.
	if hasConflict(r, "W1", "B15") || hasConflict(r, "W1", "B6") {
		t.Errorf("RTK must not be refused alongside hook_quiet_commands or hook_repeat_read: %v", r.Reasons())
	}
	if got, want := OverlappingBuiltins("rtk"), []string{"hook_large_output", "noise_normalizer", "output_trimmer"}; !reflect.DeepEqual(got, want) {
		t.Errorf("OverlappingBuiltins(rtk) = %v, want %v", got, want)
	}
}

func TestSingletonResources(t *testing.T) {
	sub := Context{Login: LoginSubscription, Have: []string{CapMCPServer}}
	api := Context{Login: LoginAPIKey, GatewayOn: true, Have: []string{CapMCPServer}}
	cases := []struct {
		name     string
		ctx      Context
		sel      []string
		a, b     string
		conflict bool
	}{
		{"two code-navigation tool sets", sub, []string{"trove_mcp", "token-savior"}, "B9", "W2", true},
		{"two brevity rule sets", sub, []string{"terse-rules", "terse_replies"}, "C13", "W3", true},
		{"two external brevity rule sets", sub, []string{"terse-rules", "caveman"}, "W3", "W4", true},
		{"a proxy and the gateway both want the endpoint", api, []string{"headroom", "duplicate_result_collapser"}, "B2", "W5", true},
		{"gateway techniques share the gateway", api, []string{"duplicate_result_collapser", "cache_breakpoints"}, "B2", "B5", false},
		{"one owner's techniques on one resource", sub, []string{"noise_normalizer", "output_trimmer"}, "B1", "B3", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasConflict(Check(c.sel, c.ctx), c.a, c.b); got != c.conflict {
				t.Errorf("conflict(%s, %s) = %v, want %v", c.a, c.b, got, c.conflict)
			}
		})
	}
}

func TestUnmetRequirementsAreExplained(t *testing.T) {
	cases := []struct {
		name string
		ctx  Context
		sel  string
		want string
	}{
		{"gateway technique on a subscription", Context{Login: LoginSubscription}, "duplicate_result_collapser", "never touches a subscription login"},
		{"gateway technique with an unknown login", Context{Login: LoginUnknown}, "duplicate_result_collapser", "cannot tell which login"},
		{"gateway technique before the gateway is set up", Context{Login: LoginAPIKey}, "duplicate_result_collapser", "trove setup --gateway"},
		{"the trimmer without the MCP server", Context{Login: LoginSubscription}, "output_trimmer", "trove mcp install"},
		{"a candidate that is not built", Context{Login: LoginAPIKey, GatewayOn: true}, "fan_out", "not built yet"},
		{"an excluded tool", Context{Login: LoginSubscription}, "supercompress", "CLAUDE.md rule 1"},
		{"a tool studied for ideas", Context{Login: LoginSubscription}, "headroom", "ideas only"},
		{"a custom-agent technique", Context{Login: LoginAPIKey, GatewayOn: true}, "cache_breakpoints", "custom agent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Check([]string{c.sel}, c.ctx)
			if r.OK() || !strings.Contains(strings.Join(r.Reasons(), " "), c.want) {
				t.Errorf("reasons %v; want one mentioning %q", r.Reasons(), c.want)
			}
		})
	}
}

func TestUnknownNamesAreReportedNotIgnored(t *testing.T) {
	r := Check([]string{"noise_normalizer", "nope", "also-nope"}, Context{Login: LoginSubscription})
	if !reflect.DeepEqual(r.Unknown, []string{"also-nope", "nope"}) || r.OK() {
		t.Errorf("unknown = %v", r.Unknown)
	}
	if got := CanAdd(nil, "nope", Context{}); !reflect.DeepEqual(got.Unknown, []string{"nope"}) {
		t.Errorf("CanAdd(nope) = %+v", got)
	}
}

func TestCanAddReportsOnlyTheProblemsOfTheAddedTechnique(t *testing.T) {
	ctx := Context{Login: LoginSubscription, Have: []string{CapMCPServer, CapRecoveryStore}}
	// terse-rules and caveman already conflict with each other; adding rtk must not be blamed for that.
	cur := []string{"terse-rules", "caveman"}
	if r := CanAdd(cur, "rtk", ctx); !r.OK() {
		t.Errorf("rtk was blamed for a problem that is not its own: %v", r.Reasons())
	}
	r := CanAdd([]string{"noise_normalizer"}, "rtk", ctx)
	if len(r.Conflicts) != 1 || !hasConflict(r, "B1", "W1") {
		t.Errorf("adding rtk beside noise_normalizer: %v", r.Reasons())
	}
}

func TestCheckIsDeterministicAndIgnoresOrderAndDuplicates(t *testing.T) {
	ctx := Context{Login: LoginSubscription, Have: []string{CapMCPServer, CapRecoveryStore}}
	a := Check([]string{"rtk", "output_trimmer", "noise_normalizer", "B1", "noise_normalizer"}, ctx)
	b := Check([]string{"noise_normalizer", "output_trimmer", "rtk"}, ctx)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("order or duplicates changed the answer:\n%+v\n%+v", a, b)
	}
}

func TestGetFindsByIDAndByName(t *testing.T) {
	for _, n := range []string{"B1", "b1", "noise_normalizer"} {
		if x, ok := Get(n); !ok || x.ID != "B1" {
			t.Errorf("Get(%q) = %v, %v", n, x.ID, ok)
		}
	}
	all := All()
	all[0].Title = "changed"
	if registry[0].Title == "changed" {
		t.Error("All returns the registry itself, not a copy")
	}
}

func TestAllIsInNumericOrder(t *testing.T) {
	var ids []string
	for _, x := range All() {
		ids = append(ids, x.ID)
	}
	idx := func(id string) int {
		for i, v := range ids {
			if v == id {
				return i
			}
		}
		return -1
	}
	if idx("B9") >= idx("B10") || idx("B14") >= idx("B15") || idx("B15") >= idx("C1") || idx("C13") >= idx("W1") {
		t.Errorf("order %v", ids)
	}
}

func TestHookNeedsAreWellFormed(t *testing.T) {
	for _, x := range registry {
		for _, n := range x.HookNeeds {
			if !ValidHookNeed(n) {
				t.Errorf("%s: hook need %q is not Tool:input or Tool:output", x.ID, n)
			}
		}
		// A technique that runs only in the hooks (no request layer) and rewrites something needs a probe;
		// one with a request layer is covered by the gateway when it is in use.
		if len(x.HookNeeds) > 0 && !x.Subscription {
			t.Errorf("%s: needs hook capabilities but is not marked as working without the gateway", x.ID)
		}
	}
	for n, want := range map[string]bool{"Bash:output": true, "Read:input": true, "bash:output": true, "Bash": false, "Bash:both": false, ":input": false, "": false} {
		if got := ValidHookNeed(n); got != want {
			t.Errorf("ValidHookNeed(%q) = %v, want %v", n, got, want)
		}
	}
}

func TestHookCapabilitiesAreRequiredOnlyWhereTheHooksAreTheWayIn(t *testing.T) {
	noProbe := Context{Login: LoginSubscription}
	probed := Context{Login: LoginSubscription, HookCaps: []string{"Bash:output", "Bash:input", "Read:input"}}
	gateway := Context{Login: LoginAPIKey, GatewayOn: true}
	cases := []struct {
		name string
		ctx  Context
		want string // "" means no problem
	}{
		// noise_normalizer has a request layer, so the gateway satisfies it; without the gateway the hooks need a probe.
		{"noise_normalizer", noProbe, "Bash output rewriting"},
		{"noise_normalizer", probed, ""},
		{"noise_normalizer", gateway, ""},
		// hook_read_limit and hook_quiet_commands exist only in the hooks, so the gateway never helps.
		{"hook_read_limit", gateway, "Read input rewriting"},
		{"hook_read_limit", probed, ""},
		{"hook_quiet_commands", noProbe, "Bash input rewriting"},
		{"hook_quiet_commands", probed, ""},
		// Denying a repeat read is not a rewrite and needs no probe.
		{"hook_repeat_read", noProbe, ""},
		// A probe for the wrong tool or kind does not count.
		{"noise_normalizer", Context{Login: LoginSubscription, HookCaps: []string{"Read:input", "Bash:input"}}, "Bash output rewriting"},
	}
	for _, c := range cases {
		r := Check([]string{c.name}, c.ctx)
		var got []string
		for _, u := range r.Unmet {
			got = append(got, u.Problem)
		}
		joined := strings.Join(got, "; ")
		if (c.want == "") != (joined == "") || !strings.Contains(joined, c.want) {
			t.Errorf("%s in %+v: problems %q, want one mentioning %q", c.name, c.ctx, joined, c.want)
		}
		if c.want != "" && !strings.Contains(joined, "trove hooks verify") {
			t.Errorf("%s: the problem must say what to do: %q", c.name, joined)
		}
	}
}

func TestAutoProblemAllowsAddedTextOnlyWhenItIsCharged(t *testing.T) {
	base := Technique{Status: StatusBuilt, Tier: tierLossless}
	withText := func(session, use *int) Technique {
		x := base
		x.AddsText, x.Overhead, x.UseOverhead = true, session, use
		return x
	}
	cases := []struct {
		name string
		x    Technique
		ok   bool
	}{
		{"plain lossless", base, true},
		{"text, both declared", withText(none(), use(68)), true},
		{"text, no per-use figure", withText(none(), nil), false},
		{"text, no per-session figure", withText(nil, use(68)), false},
		{"text, nothing declared", withText(nil, nil), false},
		{"prefix", func() Technique { x := base; x.Prefix = true; return x }(), false},
		{"not lossless", func() Technique { x := base; x.Tier = tierRecover; return x }(), false},
		{"not built", func() Technique { x := base; x.Status = StatusExcluded; return x }(), false},
	}
	for _, c := range cases {
		if got := autoProblem(c.x) == ""; got != c.ok {
			t.Errorf("%s: eligible = %v, want %v (%q)", c.name, got, c.ok, autoProblem(c.x))
		}
	}
}

func TestOverheadOfReadsTheRegistry(t *testing.T) {
	if o, ok := OverheadOf("hook_repeat_read"); !ok || o.PerUse != 68 || o.Netted || o.PerSession != 0 {
		t.Errorf("hook_repeat_read: %+v %v", o, ok)
	}
	if o, ok := OverheadOf("hook_read_limit"); !ok || o.PerUse != 42 || !o.Netted {
		t.Errorf("hook_read_limit: %+v %v", o, ok)
	}
	if o, ok := OverheadOf("noise_normalizer"); !ok || o != (Overhead{}) {
		t.Errorf("noise_normalizer adds no text: %+v %v", o, ok)
	}
	if _, ok := OverheadOf("output_trimmer"); ok {
		t.Error("output_trimmer adds text with no numbers declared, so it must not report a usable overhead")
	}
	if _, ok := OverheadOf("nope"); ok {
		t.Error("an unknown name has no overhead")
	}
}
