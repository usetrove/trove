package cli

import (
	"context"
	"fmt"
	"github.com/usetrove/trove/internal/mcp/install"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/optimize/suggest"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

type optimizerInfo struct {
	Name       string `json:"name"`
	Tier       string `json:"tier"`
	Configured string `json:"configured"`
	Effective  string `json:"effective"`
	Note       string `json:"note,omitempty"`
}

type optimizeStatus struct {
	KillSwitch   killSwitch      `json:"kill_switch"`
	CustomAgents []string        `json:"custom_agents"`
	BudgetMS     int             `json:"budget_ms"`
	Optimizers   []optimizerInfo `json:"optimizers"` // in the order they run
	// Gateway lists behaviors of the gateway itself (in-flight dedup). They are not part of the run order:
	// they act on which requests reach the provider, not on a request's bytes.
	Gateway []optimizerInfo `json:"gateway,omitempty"`
	Unknown []string        `json:"unknown_optimizers,omitempty"`
}

type killSwitch struct {
	On     bool   `json:"on"`
	Reason string `json:"reason,omitempty"`
}

func newOptimize(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "optimize",
		Short: "See optimizer settings, stop all optimizers, or get suggestions",
		Long: "Optimizers rewrite requests that pass through the gateway to remove waste. Each is off by default;\n" +
			"turn one to \"shadow\" (estimate only) or \"on\" under [optimizers] in config.toml. A change applies to\n" +
			"new sessions only, never mid-session.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(
		&cobra.Command{Use: "status", Short: "Show each optimizer's state and the kill switch", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return optimizeStatusCmd(a) }},
		&cobra.Command{Use: "off", Short: "Stop every optimizer immediately (kill switch)", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return optimizeOff(a) }},
		&cobra.Command{Use: "resume", Short: "Undo `trove optimize off`", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return optimizeResume(a) }},
		newOptimizeSuggest(a),
	)
	return cmd
}

func optimizeStatusCmd(a *App) error {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	on, why := optimize.Disabled(optimize.KillFile(dir), a.Getenv)
	st := optimizeStatus{
		KillSwitch:   killSwitch{On: on, Reason: why},
		CustomAgents: cfg.Optimize.CustomAgents,
		BudgetMS:     cfg.Optimize.BudgetMS,
	}
	recoverable := install.Registered(dir)
	known := map[string]bool{}
	for _, o := range optimize.Pipeline() {
		known[o.Name()] = true
	}
	for _, name := range optimize.PipelineNames {
		info := optimizerInfo{Name: name, Configured: cfg.OptimizerState(name)}
		info.Effective = optimize.EffectiveStateFor(name, info.Configured, recoverable)
		for _, o := range optimize.Pipeline() {
			if o.Name() == name {
				info.Tier = string(o.Tier())
			}
		}
		switch name {
		case config.OptimizerOutputTrimmer:
			info.Note = "needs the Trove MCP server so your agent can fetch trimmed output back; estimate-only (shadow) until `trove mcp install`"
			if recoverable {
				info.Note = "trimmed output is stored locally and the agent can fetch it back with trove_fetch_full"
			}
		case "cache_breakpoints", "volatile_prefix_mover":
			info.Note = "custom agents only"
			if len(cfg.Optimize.CustomAgents) == 0 {
				info.Note += " (none marked: set optimize.custom_agents)"
			}
		}
		st.Optimizers = append(st.Optimizers, info)
	}
	for _, name := range optimize.GatewayNames {
		known[name] = true
		info := optimizerInfo{Name: name, Tier: "gateway", Configured: cfg.OptimizerState(name)}
		info.Effective = optimize.EffectiveStateFor(name, info.Configured, recoverable)
		switch name {
		case config.OptimizerInflightDedup:
			info.Note = "estimate only: counts identical requests that arrive while one is still waiting for the provider; sharing the call is not built, so \"on\" runs as shadow"
		case config.OptimizerSpendCap:
			info.Note = "estimate only: counts requests made after spending reached your budget cap, which a hard cap would have refused; pausing is not built, so \"on\" runs as shadow"
			if cfg.Budget.CapUSD <= 0 {
				info.Note += ". It needs a budget cap: `trove budget set <amount>`"
			}
		}
		st.Gateway = append(st.Gateway, info)
	}
	for n := range cfg.Optimizers {
		if !known[n] {
			st.Unknown = append(st.Unknown, n)
		}
	}
	sort.Strings(st.Unknown)
	return a.emit(st, renderOptimizeStatus(st))
}

func renderOptimizeStatus(st optimizeStatus) string {
	var b strings.Builder
	if st.KillSwitch.On {
		fmt.Fprintf(&b, "Kill switch: ON (%s). No request is being changed.\n\n", st.KillSwitch.Reason)
	} else {
		b.WriteString("Kill switch: off. `trove optimize off` stops every optimizer at once.\n\n")
	}
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "RUN ORDER\tOPTIMIZER\tTIER\tCONFIGURED\tEFFECTIVE\tNOTE")
	for i, o := range st.Optimizers {
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", i+1, o.Name, o.Tier, o.Configured, o.Effective, o.Note)
	}
	_ = w.Flush()
	if len(st.Gateway) > 0 {
		b.WriteString("\nGateway behaviors (not part of the run order; they act on which requests reach the provider):\n")
		gw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(gw, "BEHAVIOR\tCONFIGURED\tEFFECTIVE\tNOTE")
		for _, o := range st.Gateway {
			_, _ = fmt.Fprintf(gw, "%s\t%s\t%s\t%s\n", o.Name, o.Configured, o.Effective, o.Note)
		}
		_ = gw.Flush()
	}
	fmt.Fprintf(&b, "\nCustom agents: %s. Time budget per request: %d ms; past it, your original request is forwarded.\n",
		orNone(st.CustomAgents), st.BudgetMS)
	b.WriteString("States are fixed when a session starts; edits to config.toml apply to the next session.\n")
	if len(st.Unknown) > 0 {
		fmt.Fprintf(&b, "Ignored (no such optimizer): %s\n", strings.Join(st.Unknown, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

type optimizeResult struct {
	KillSwitch bool   `json:"kill_switch"`
	Message    string `json:"message"`
}

func optimizeOff(a *App) error {
	dir, err := a.ResolveDataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %v. Check its permissions", dir, err)
	}
	if err := os.WriteFile(optimize.KillFile(dir), nil, 0o600); err != nil {
		return fmt.Errorf("cannot write the kill-switch file: %v. Set %s=off in the gateway's environment instead", err, config.EnvOptimize)
	}
	msg := "All optimizers are off. A running gateway notices on its next request and forwards requests unchanged.\n" +
		"Switching off mid-session changes what the provider sees, so expect one cache re-write on the next turn.\n" +
		"Run `trove optimize resume` to turn them back on."
	return a.emit(optimizeResult{KillSwitch: true, Message: msg}, msg)
}

func optimizeResume(a *App) error {
	dir, err := a.ResolveDataDir()
	if err != nil {
		return err
	}
	if err := os.Remove(optimize.KillFile(dir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot remove the kill-switch file: %v. Delete %s by hand", err, optimize.KillFile(dir))
	}
	msg := "The kill switch is off. Optimizers run as configured in config.toml."
	on, _ := optimize.Disabled("", a.Getenv)
	if on {
		msg = "Removed the kill-switch file, but " + config.EnvOptimize + " is still set to off in this environment, which keeps every optimizer disabled."
	}
	return a.emit(optimizeResult{KillSwitch: on, Message: msg}, msg)
}

type suggestOutput struct {
	From        string               `json:"from"`
	To          string               `json:"to"`
	Basis       string               `json:"basis"`
	Suggestions []suggest.Suggestion `json:"suggestions"`
}

func newOptimizeSuggest(a *App) *cobra.Command {
	var days int
	c := &cobra.Command{
		Use:   "suggest",
		Short: "Recommendations Trove never applies on its own",
		Long: "Suggests a cheaper model for small tasks, the Batch API for work that can wait, and a lower reasoning\n" +
			"effort, using only the metadata Trove has recorded. Every figure is an estimate; nothing is changed.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if days < 1 || days > 3650 {
				return fmt.Errorf("--days is %d; use a number from 1 to 3650", days)
			}
			dir, err := a.ResolveDataDir()
			if err != nil {
				return err
			}
			to := time.Now()
			out := suggestOutput{From: to.AddDate(0, 0, -days).UTC().Format(time.DateOnly), To: to.UTC().Format(time.DateOnly),
				Basis: "estimated: from recorded metadata and published list prices; nothing is applied automatically", Suggestions: []suggest.Suggestion{}}
			if _, err := os.Stat(filepath.Join(dir, store.DBFileName)); err != nil {
				return a.emit(out, "No usage recorded yet, so there is nothing to suggest. Run your agents through `trove gateway` first.")
			}
			db, err := store.Open(dir)
			if err != nil {
				return fmt.Errorf("cannot open Trove's database: %v. Run `trove doctor` for details", err)
			}
			defer func() { _ = db.Close() }()
			tbl, err := pricing.Default()
			if err != nil {
				tbl = nil // suggestions that need prices are skipped
			}
			ss, err := suggest.Generate(context.Background(), db, tbl, to.AddDate(0, 0, -days), to.Add(time.Hour))
			if err != nil {
				return fmt.Errorf("cannot read recorded usage: %v. Run `trove doctor` for details", err)
			}
			out.Suggestions = append(out.Suggestions, ss...)
			return a.emit(out, renderSuggestions(out))
		},
	}
	c.Flags().IntVar(&days, "days", 30, "how many days of history to look at")
	return c
}

func renderSuggestions(o suggestOutput) string {
	if len(o.Suggestions) == 0 {
		return fmt.Sprintf("No suggestions for %s to %s: not enough recorded usage to say anything useful.", o.From, o.To)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Suggestions for %s to %s (all estimated; Trove changes nothing on its own)\n", o.From, o.To)
	for i, s := range o.Suggestions {
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, s.Title, s.Detail)
		switch {
		case s.EstSavingsUSD != nil:
			fmt.Fprintf(&b, "   Estimated saving: $%.2f over %d requests (confidence: %s)\n", *s.EstSavingsUSD, s.Requests, s.Confidence)
		case s.Scenario != "":
			fmt.Fprintf(&b, "   No savings figure is published; %s (confidence: %s)\n", s.Scenario, s.Confidence)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
