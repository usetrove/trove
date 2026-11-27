package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/contribute"
)

// limitsExplained says in plain words what the limits section holds (docs/decisions/limit-report.md).
const limitsExplained = "The \"limits\" part says: the plan you named; your most used Claude Code version; for each plan limit, how much\n" +
	"output, cache reads and cache writes seemed to count compared with input (\"ratio\"), how unsure that is (\"width\",\n" +
	"the width of the 90% interval) and what share of the limit's use Trove could not explain (\"unexplained_pct\");\n" +
	"and, for up to 5 recent Claude Code versions, whether the regression watch flagged them. All of it is estimated."

// limitsConsent is the text shown before asking (docs/decisions/limit-report.md, approved 2026-11-26).
const limitsConsent = "Share your plan-limit estimate too?\n\n" +
	"This adds a \"limits\" section to the weekly summary. It says how much output, cache reads and cache writes\n" +
	"seemed to count toward your plan limits compared with input, how sure that estimate is, what share of your\n" +
	"limit use Trove could not explain, the plan you named, your main Claude Code version, and whether recent\n" +
	"versions used your limit faster than earlier ones.\n\n" +
	"It never includes your prompts, code, file paths, session ids, model names, token counts or any time finer\n" +
	"than the day it arrives.\n\n" +
	"The project publishes only medians and ranges over at least 20 installs on a weekly page. Your numbers are\n" +
	"never shown on their own. Once a weekly page is published, its figures can't be withdrawn, but\n" +
	"`trove contribute delete` removes your rows so later pages leave you out.\n"

type contributeLimitsStatus struct {
	Enabled bool   `json:"enabled"`
	Limits  bool   `json:"limits"`
	Plan    string `json:"plan,omitempty"`
}

func newContributeLimits(a *App) *cobra.Command {
	c := &cobra.Command{
		Use:   "limits",
		Short: "Add (or stop adding) your plan-limit estimate to the shared summary",
		Long: "The plan-limit section is a separate opt-in on top of `trove contribute enable`. It holds ratios, interval\n" +
			"widths and whole percents from your own plan-limit estimate, the plan you name, and per-version regression\n" +
			"flags. See `trove contribute --preview` for the exact bytes.",
		Args: cobra.NoArgs,
	}
	c.AddCommand(newContributeLimitsEnable(a), newContributeLimitsDisable(a))
	return c
}

func newContributeLimitsEnable(a *App) *cobra.Command {
	var pf periodFlags
	var plan string
	var yes bool
	c := &cobra.Command{
		Use:     "enable",
		Short:   "Add your plan-limit estimate to the weekly summary",
		Long:    "Shows what is added and the exact summary, then asks. Needs `trove contribute enable` first.",
		Example: "  trove contribute limits enable --plan max",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if !contribute.ValidPlan(plan) {
				return fmt.Errorf("name your plan with --plan, one of: %s. Trove cannot find it out for itself, and use unspecified if you'd rather not say",
					strings.Join(contribute.Plans, ", "))
			}
			dir, _, err := loadConfig(a)
			if err != nil {
				return err
			}
			st, err := contribute.LoadState(dir)
			if err != nil {
				return err
			}
			if !st.Enabled || st.InstallID == "" {
				return fmt.Errorf("sharing is off, so there is nothing to add this to. Run `trove contribute enable` first")
			}
			r, err := buildReport(a, pf)
			if err != nil {
				return err
			}
			p := payloadOf(r, plan)
			p.InstallID = st.InstallID
			body, err := contribute.Encode(p)
			if err != nil {
				return err
			}
			if !a.JSON {
				note := ""
				if p.Limits == nil {
					note = "There is no estimate to add yet, so this week's summary would not change. Once there is, it appears under \"limits\".\n\n"
				}
				_, _ = fmt.Fprintf(a.Out, "%s\nWith it on, the summary would be exactly:\n\n%s\n%s", limitsConsent, body, note)
			}
			if !yes && !a.confirm("Share it?") {
				return a.emit(contributeLimitsStatus{Enabled: st.Enabled, Limits: st.Limits, Plan: st.Plan}, "Nothing changed. Run again and answer y, or pass --yes.")
			}
			st.Limits, st.Plan = true, plan
			if err := contribute.SaveState(dir, st); err != nil {
				return err
			}
			return a.emit(contributeLimitsStatus{Enabled: true, Limits: true, Plan: plan},
				"The plan-limit section is on. See it with `trove contribute --preview`; stop it with `trove contribute limits disable`.")
		},
	}
	pf.add(c)
	c.Flags().StringVar(&plan, "plan", "", "your plan: "+strings.Join(contribute.Plans, ", "))
	c.Flags().BoolVar(&yes, "yes", false, "turn on without asking")
	return c
}

func newContributeLimitsDisable(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Stop adding your plan-limit estimate to the summary",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, _, err := loadConfig(a)
			if err != nil {
				return err
			}
			st, err := contribute.LoadState(dir)
			if err != nil {
				return err
			}
			st.Limits, st.Plan = false, ""
			if err := contribute.SaveState(dir, st); err != nil {
				return err
			}
			return a.emit(contributeLimitsStatus{Enabled: st.Enabled},
				"The plan-limit section is off: later uploads leave it out. To remove what was already sent, run `trove contribute delete`.")
		},
	}
}
