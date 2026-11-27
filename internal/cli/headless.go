package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/gateway"
)

// `trove headless`: run CI, cron and scripted `claude -p` jobs on the monthly API credits a Max, Ultra or Team plan
// includes, through Trove's gateway, so they stop using the plan's limits. It only prints text: it never reads,
// stores or moves a key or a login (PRINCIPLES.md rules 2, 3 and 10), and never changes an interactive session's
// settings. Facts verified 2026-11-24, see docs/headless.md:
//   - credits: support.claude.com/en/articles/17154008 (Max $100, Ultra $200, Team $20/$100 per seat pooled up to
//     $500; Free, Pro and Enterprise none; they cover `claude -p` and the Agent SDK only with an API key from the
//     linked Console organisation; not interactive Claude Code, and not runs the Claude Code GitHub Action starts,
//     even with -p; when they run out, requests stop unless the organisation has bought credits or auto-reload);
//   - `--bare`: "never reads OAuth credentials or the system keychain", and skips hooks, plugins, MCP servers, auto
//     memory and CLAUDE.md (code.claude.com/docs/en/headless);
//   - in -p mode ANTHROPIC_API_KEY "is always used when present" (code.claude.com/docs/en/iam);
//   - installers: claude.ai/install.sh (code.claude.com/docs/en/setup) and Trove's install.sh, both to ~/.local/bin.

// HeadlessKeyVar is the variable the snippets read the API key from. The user fills it from their own secret store.
const HeadlessKeyVar = "HEADLESS_API_KEY"

// headlessTradeoff is the trade-off, in two lines.
var headlessTradeoff = []string{
	"Headless jobs run with an API key from the Console organisation linked to your Max, Ultra or Team plan use its monthly API credits instead of your plan's limits; interactive sessions keep your plan login.",
	"When the credits run out those jobs stop until next month unless that organisation has bought credits or auto-reload, and --bare skips your hooks, plugins, MCP servers and CLAUDE.md.",
}

// headlessSources are where the facts above were checked, and when.
var headlessSources = []string{
	"support.claude.com/en/articles/17154008 (monthly API credits), read 2026-11-24",
	"code.claude.com/docs/en/headless (--bare, unattended runs), read 2026-11-24",
	"code.claude.com/docs/en/iam (ANTHROPIC_API_KEY in -p mode), read 2026-11-24",
	"code.claude.com/docs/en/setup (install), read 2026-11-24",
}

// HeadlessEnvSnippet is a shell function that runs one job through the gateway at base with the API key from
// HeadlessKeyVar. The variables are set for that command only.
func HeadlessEnvSnippet(base string) string {
	return `# trove headless: run headless Claude Code jobs (cron, scripts) on the monthly API credits your
# Max, Ultra or Team plan includes, through Trove's local gateway. Interactive sessions are not touched:
# the variables below are set for the job's command only.
#
# 1. Create an API key in the Console organisation linked to your plan. Keep it in your secret store and
#    give it to the job as ` + HeadlessKeyVar + `. Trove never reads, stores or moves it.
# 2. Keep the gateway running: trove gateway start --background
#
# --bare makes Claude Code use only the API key: it never reads your login or keychain. It also skips hooks,
# plugins, MCP servers, auto memory and CLAUDE.md; pass --append-system-prompt-file or --settings if a job
# needs them. When the credits run out the job's requests stop (unless your organisation has bought credits
# or auto-reload); they are never charged to your plan.
headless_job() {
  ANTHROPIC_BASE_URL="` + base + `" \
  ANTHROPIC_API_KEY="${` + HeadlessKeyVar + `:?set ` + HeadlessKeyVar + ` to an API key from the Console organisation linked to your plan}" \
    claude --bare -p "$@"
}
# Example: headless_job "Summarize README.md" --allowedTools "Read"
`
}

// HeadlessCIExample is a GitHub Actions workflow that installs Trove and Claude Code, starts the gateway on port and
// runs one job through it with the key from the repository secret HeadlessKeyVar.
func HeadlessCIExample(port int) string {
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	return `# trove headless: a CI job that runs claude -p on your plan's monthly API credits through Trove's gateway.
# Use a plain shell step like this one, not the Claude Code GitHub Action: runs that action starts count as
# Claude Code usage, which the credits don't cover, even with -p.
# Store an API key from the Console organisation linked to your plan as the repository secret ` + HeadlessKeyVar + `.
# The gateway keeps its records on the runner, which discards them when the job ends.
name: headless-job
on:
  workflow_dispatch:
jobs:
  headless:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4 # use the version your other workflows pin
      - name: Install Trove and Claude Code
        run: |
          curl -fsSL https://troverelay.com/install.sh | sh
          curl -fsSL https://claude.ai/install.sh | bash
          echo "$HOME/.local/bin" >> "$GITHUB_PATH"
      - name: Start Trove's gateway
        run: |
          trove gateway start > "$RUNNER_TEMP/trove-gateway.log" 2>&1 &
          for i in $(seq 1 30); do
            curl -fsS ` + base + gateway.HealthPath + ` > /dev/null && exit 0
            sleep 1
          done
          echo "Trove's gateway did not start; see $RUNNER_TEMP/trove-gateway.log" >&2
          exit 1
      - name: Run the job
        env:
          ANTHROPIC_BASE_URL: ` + base + gateway.PrefixAnthropic + `
          ANTHROPIC_API_KEY: ${{ secrets.` + HeadlessKeyVar + ` }}
        run: claude --bare -p "Summarize README.md" --allowedTools "Read"
`
}

type headlessOutput struct {
	Tradeoff []string `json:"tradeoff"`
	BaseURL  string   `json:"base_url"`
	KeyVar   string   `json:"key_variable"`
	Env      string   `json:"env_snippet,omitempty"`
	CI       string   `json:"ci_example,omitempty"`
	Sources  []string `json:"sources"`
}

func newHeadless(a *App) *cobra.Command {
	var port int
	run := func(env, ci bool) error {
		_, cfg, err := loadConfig(a)
		if err != nil {
			return err
		}
		if port == 0 {
			port = cfg.Gateway.Port
		}
		if port < 1 || port > 65535 {
			return fmt.Errorf("--port is %d; use a port from 1 to 65535", port)
		}
		base := fmt.Sprintf("http://127.0.0.1:%d%s", port, gateway.PrefixAnthropic)
		out := headlessOutput{Tradeoff: headlessTradeoff, BaseURL: base, KeyVar: HeadlessKeyVar, Sources: headlessSources}
		var b strings.Builder
		for _, l := range headlessTradeoff {
			b.WriteString(l + "\n")
		}
		if env {
			out.Env = HeadlessEnvSnippet(base)
			b.WriteString("\n" + out.Env)
		}
		if ci {
			out.CI = HeadlessCIExample(port)
			b.WriteString("\n" + out.CI)
		}
		if !env && !ci {
			b.WriteString("\n  trove headless env   a shell function for cron jobs and scripts on this machine\n" +
				"  trove headless ci    a GitHub Actions workflow that runs the job through the gateway on the runner\n" +
				"\nSee docs/headless.md for the steps and the sources.")
		}
		return a.emit(out, strings.TrimRight(b.String(), "\n"))
	}
	c := &cobra.Command{
		Use:   "headless",
		Short: "Run claude -p, cron and CI jobs on your plan's monthly API credits, through the gateway",
		Long: "Prints how to run headless Claude Code jobs with an API key from the Console organisation linked to your\n" +
			"Max, Ultra or Team plan, through Trove's gateway, so they use the plan's monthly API credits instead of its limits.\n" +
			"It only prints: it never reads, stores or moves a key or a login, and never changes an interactive session.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return run(false, false) },
	}
	c.PersistentFlags().IntVar(&port, "port", 0, "the gateway's port (default gateway.port in config.toml)")
	c.AddCommand(&cobra.Command{
		Use: "env", Short: "Print a shell function that runs one job through the gateway with the API key",
		Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return run(true, false) },
	}, &cobra.Command{
		Use: "ci", Short: "Print a GitHub Actions workflow that runs a job through the gateway on the runner",
		Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return run(false, true) },
	})
	return c
}
