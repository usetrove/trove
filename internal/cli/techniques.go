package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/technique"
)

// techniquesOutput is the --json shape of `trove techniques`.
type techniquesOutput struct {
	Techniques []technique.Technique `json:"techniques"`
}

// checkOutput is the --json shape of `trove techniques check`.
type checkOutput struct {
	Selected []string         `json:"selected"`
	OK       bool             `json:"ok"`
	Result   technique.Result `json:"result"`
	Reasons  []string         `json:"reasons,omitempty"`
}

func newTechniques(a *App) *cobra.Command {
	c := &cobra.Command{
		Use:   "techniques",
		Short: "List every token-saving technique Trove knows, and check whether a set can run together",
		Long: "Lists each technique with where it acts, how Trove gets it, whether it can change the cached prompt, and which\n" +
			"logins it works for. `trove techniques check <name>...` says whether the named techniques can all be on at once,\n" +
			"in plain English. It changes nothing. Names are IDs (B1) or optimizer, fix or tool names (noise_normalizer, rtk).",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runTechniques(a) },
	}
	var login string
	var hookProbe []string
	var gateway, mcp, custom bool
	check := &cobra.Command{
		Use:   "check <name>...",
		Short: "Say whether these techniques can all be on together, in your setup",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			l := technique.Login(login)
			switch l {
			case technique.LoginAPIKey, technique.LoginSubscription, technique.LoginUnknown:
			default:
				return fmt.Errorf("--login is %q; use api_key, subscription or unknown", login)
			}
			for _, n := range hookProbe {
				if !technique.ValidHookNeed(n) {
					return fmt.Errorf("--hook-probe has %q; write each as Tool:input or Tool:output, for example Bash:output", n)
				}
			}
			ctx := technique.Context{Login: l, GatewayOn: gateway, HookCaps: hookProbe}
			if mcp {
				ctx.Have = append(ctx.Have, technique.CapMCPServer, technique.CapRecoveryStore)
			}
			if custom {
				ctx.Have = append(ctx.Have, technique.CapCustomAgent)
			}
			return runTechniqueCheck(a, args, ctx)
		},
	}
	check.Flags().StringVar(&login, "login", string(technique.LoginUnknown), "how you sign in: api_key, subscription or unknown")
	check.Flags().BoolVar(&gateway, "gateway", false, "the Trove gateway is set up")
	check.Flags().BoolVar(&mcp, "mcp", false, "Trove's MCP server is registered with your agent")
	check.Flags().BoolVar(&custom, "custom-agent", false, "you use a custom agent whose prompt Trove may lay out")
	check.Flags().StringSliceVar(&hookProbe, "hook-probe", nil, "hook rewrites a probe has seen work, as Tool:kind, for example Bash:output,Read:input (see `trove hooks verify`)")
	c.AddCommand(check)
	return c
}

func runTechniques(a *App) error {
	all := technique.All()
	var b strings.Builder
	fmt.Fprintf(&b, "%-4s %-9s %-34s %-7s %-6s %-5s %s\n", "ID", "STATUS", "WHAT", "PROMPT", "LOGIN", "GATE", "ROUTE")
	for _, t := range all {
		login := "any"
		switch {
		case t.Gateway:
			login = "key"
		case !t.Subscription:
			login = "-"
		}
		prompt := "keeps"
		if t.Prefix {
			prompt = "may change"
		}
		gate := ""
		if t.Gateway {
			gate = "yes"
		}
		fmt.Fprintf(&b, "%-4s %-9s %-34s %-7s %-6s %-5s %s\n", t.ID, t.Status, clip(t.Title, 34), prompt, login, gate, t.Route)
	}
	b.WriteString("\nPROMPT: whether it can change the cached prompt. LOGIN: any = works with a subscription or a key; key = needs the gateway (API key only).\n" +
		"Run `trove techniques --json` for every field, or `trove techniques check <name>...` to test a set.")
	return a.emit(techniquesOutput{Techniques: all}, b.String())
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func runTechniqueCheck(a *App, names []string, ctx technique.Context) error {
	res := technique.Check(names, ctx)
	out := checkOutput{Selected: names, OK: res.OK(), Result: res, Reasons: res.Reasons()}
	var b strings.Builder
	if res.OK() {
		b.WriteString("These can all be on together" + setupNote(ctx) + ".")
	} else {
		b.WriteString("These cannot all be on together" + setupNote(ctx) + ":\n")
		for _, r := range res.Reasons() {
			b.WriteString("  - " + r + "\n")
		}
	}
	return a.emit(out, strings.TrimRight(b.String(), "\n"))
}

func setupNote(ctx technique.Context) string {
	return fmt.Sprintf(" (login: %s, gateway: %v)", ctx.Login, ctx.GatewayOn)
}
