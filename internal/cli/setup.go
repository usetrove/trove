package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/fixer"
	"github.com/usetrove/trove/internal/gateway/auth"
	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/setup"
)

type setupPlanJSON struct {
	Agent       string `json:"agent"`
	Path        string `json:"path,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Adds        string `json:"adds,omitempty"`
	Note        string `json:"note,omitempty"`
	AlreadyDone bool   `json:"already_configured,omitempty"`
	Problem     string `json:"problem,omitempty"`
	// Optional is set for an agent that is installed but not pointed at the gateway, because the user did
	// not ask for it. It is information, not a problem.
	Optional string `json:"optional,omitempty"`
}

type setupReport struct {
	Plans   []setupPlanJSON `json:"plans"`
	Applied bool            `json:"applied"`
	Changes []setup.Change  `json:"changes,omitempty"`
	// StatusLine is the optional Claude Code status-line step; nil when Claude
	// Code is not set up.
	StatusLine        *setupPlanJSON `json:"status_line,omitempty"`
	StatusLineApplied bool           `json:"status_line_applied,omitempty"`
	Verify            []string       `json:"verify,omitempty"`
	TurnOff           []string       `json:"turn_off,omitempty"`
	// Explain says what Trove records, that nothing leaves the machine, that it
	// starts observe-only, and how to see results.
	Explain []string `json:"explain,omitempty"`
	// Options explains who the gateway is for, how API credits on Max, Ultra and
	// Team plans fit, which cloud backends it handles, and the healthcare caveat.
	Options []string `json:"options,omitempty"`
}

// claudeNeedsAPIKey returns an empty string when there is evidence that Claude Code will authenticate with an
// API key, and otherwise the plain-English reason setup will not point it at the gateway. The evidence is the same
// non-credential signals the history audit uses (presence of ANTHROPIC_API_KEY, apiKeyHelper, a Console login in
// settings), or the user's own statement in config.toml ([history] plan = "api_key"). A bearer-only
// ANTHROPIC_AUTH_TOKEN is not enough: the gateway refuses a bearer without an x-api-key.
func claudeNeedsAPIKey(a *App, cfgDir, planOverride string) string {
	// A key that is set must be an accepted Console key, the same check the gateway and the live probe use.
	// A key of another kind (an OAuth token, an admin key, an unknown prefix) is refused with the reason, and
	// the key's value is never printed.
	if key := a.Getenv("ANTHROPIC_API_KEY"); key != "" {
		if ok, reason := auth.ClassifyAnthropicKey(key); !ok {
			return "Not changed. ANTHROPIC_API_KEY is set, but Trove does not accept it: " + keyRefusal(reason) +
				" Trove accepts Claude Console API keys (sk-ant-api03-... or sk-ant-usr-...). " +
				"Set a Console key in the environment you start Claude Code from and run `trove setup` again."
		}
	}
	res := logs.DetectClaudeCodePlan(a.Getenv, cfgDir, planOverride)
	if res.Plan == logs.PlanAPIKey {
		for _, ev := range res.Evidence {
			if ev != "env:ANTHROPIC_AUTH_TOKEN" {
				return ""
			}
		}
	}
	return "Not changed. Nothing says Claude Code here uses an API key, and if it uses a Claude.ai login (Free, Pro, Max, Ultra, Team or Enterprise), " +
		"pointing it at the gateway would send that login token to the gateway before the gateway refuses it. Trove never handles those logins. " +
		"If you use an API key, set ANTHROPIC_API_KEY in the environment you start Claude Code from (or an apiKeyHelper in its settings) and run `trove setup` again; " +
		"if you set the key some other way, add `plan = \"api_key\"` under [history] in config.toml. " +
		"If you use a Claude.ai plan, use the Trove plugin instead; it needs no gateway."
}

// keyRefusal says, in plain English, why a key was refused. It never includes the key.
func keyRefusal(reason string) string {
	switch reason {
	case auth.ReasonOAuthToken:
		return "it is an OAuth token (sk-ant-oat...), the kind a claude.ai login uses, and Trove never handles those."
	case auth.ReasonAdminKey:
		return "it is an Admin API key, which is not for Claude Code."
	default:
		return "its format is not one Trove recognises."
	}
}

// codexNeedsAPIKey is the Codex counterpart of claudeNeedsAPIKey. Trove cannot tell a ChatGPT login from an API key
// without opening ~/.codex/auth.json, which it never does, so the only evidence it accepts is OPENAI_API_KEY being set
// or the user's own `plan = "api_key"` in config.toml. Anything else is refused: the provider setup writes tells Codex
// to read its key from OPENAI_API_KEY, and with no key there Trove will not guess.
func codexNeedsAPIKey(a *App, planOverride string) string {
	if logs.DetectCodexPlan(a.Getenv, planOverride).Plan == logs.PlanAPIKey {
		return ""
	}
	return "Not changed. Nothing says Codex CLI here uses an OpenAI API key, and if you use Sign in with ChatGPT (or a Codex access token), " +
		"Trove never handles that login: the gateway refuses it and Trove will not read ~/.codex/auth.json to find out. " +
		"If you use an API key, set OPENAI_API_KEY in the environment you start Codex from and run `trove setup` again; " +
		"if you set the key some other way, add `plan = \"api_key\"` under [history] in config.toml. " +
		"If you use a ChatGPT plan, Trove's gateway cannot help; use the Trove hooks and MCP tools instead."
}

func newSetup(a *App) *cobra.Command {
	var yes, statusLine, gateway bool
	c := &cobra.Command{
		Use:   "setup",
		Short: "Find your coding agents and, with --gateway, point them at the Trove gateway (shows the change first; asks before applying)",
		Long: "Detects Claude Code and Codex CLI. Pointing them at the Trove gateway is opt-in: it happens only with --gateway,\n" +
			"and then Trove shows the exact change and applies it only with your consent. The original file is backed up and\n" +
			"`trove undo` reverses everything. Without --gateway, setup changes no agent's gateway setting.\n" +
			"Shell rc files are never touched. Trove never reads or writes your API keys.\n" +
			"For Claude Code it then offers, as a separate step, to show your spend and budget pace in the status line.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runSetup(a, yes, statusLine, gateway) },
	}
	c.Flags().BoolVar(&yes, "yes", false, "apply the changes shown without asking")
	c.Flags().BoolVar(&statusLine, "status-line", false, "also add Trove to Claude Code's status line without asking (--yes alone does not)")
	c.Flags().BoolVar(&gateway, "gateway", false, "opt in to pointing Claude Code and Codex CLI at the Trove gateway (required; without it setup changes no gateway setting)")
	return c
}

// gatewayOptional is shown for each installed agent when setup runs without --gateway. The gateway is an
// extra the user can add, not a step that failed.
const gatewayOptional = "Optional: route this agent through the Trove gateway with `trove setup --gateway`. Nothing is changed unless you ask."

func runSetup(a *App, yes, statusLine, gateway bool) error {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	home, err := a.HomeDir()
	if err != nil {
		return errors.New("cannot find your home directory; set HOME and try again")
	}
	var edits []*setup.Edit
	rep := setupReport{}
	claudeDir := "" // set when Claude Code is installed and its settings can be edited
	for _, spec := range supportedAgents {
		cfgDir := spec.configDir(a, home)
		_, onPath := a.LookPath(spec.binary)
		_, statErr := os.Stat(cfgDir)
		if onPath != nil && statErr != nil {
			continue // not installed
		}
		if !gateway {
			// Opt-in: no gateway setting is planned unless the user asked for it. Claude Code's
			// folder is still recorded, because the separate status-line step uses it.
			rep.Plans = append(rep.Plans, setupPlanJSON{Agent: spec.name, Optional: gatewayOptional})
			if spec.binary != "codex" {
				claudeDir = cfgDir
			}
			continue
		}
		var e *setup.Edit
		var perr error
		if spec.binary == "codex" {
			e, perr = setup.PlanCodex(cfgDir, cfg.Gateway.Port)
			if perr == nil && !e.NoOp {
				// Same rule as Claude Code below: no sign of an OpenAI API key, no change (rules 3 and 10).
				if why := codexNeedsAPIKey(a, cfg.History.Plan); why != "" {
					rep.Plans = append(rep.Plans, setupPlanJSON{Agent: spec.name, Problem: why})
					continue
				}
			}
		} else {
			e, perr = setup.PlanClaudeCode(cfgDir, cfg.Gateway.Port, cfg.Upstreams.Anthropic)
			if perr == nil && !e.NoOp {
				// Pointing Claude Code at the gateway without an API key would send its Claude.ai login token
				// to the gateway before the gateway refuses it. Never create that situation (rules 3 and 10).
				if why := claudeNeedsAPIKey(a, cfgDir, cfg.History.Plan); why != "" {
					rep.Plans = append(rep.Plans, setupPlanJSON{Agent: spec.name, Problem: why})
					continue
				}
			}
			if perr == nil {
				claudeDir = cfgDir
			}
		}
		switch {
		case perr != nil:
			rep.Plans = append(rep.Plans, setupPlanJSON{Agent: spec.name, Problem: perr.Error()})
		default:
			rep.Plans = append(rep.Plans, setupPlanJSON{Agent: spec.name, Path: e.Path, Summary: e.Summary, Adds: e.Show, Note: e.Note, AlreadyDone: e.NoOp})
			edits = append(edits, e)
		}
	}
	if len(rep.Plans) == 0 {
		return a.emit(rep, "No supported agents found (looked for Claude Code and Codex CLI). Install one, or point any API client at\n  Anthropic: "+
			setup.GatewayURL(cfg.Gateway.Port, "/anthropic")+"\n  OpenAI:    "+setup.GatewayURL(cfg.Gateway.Port, "/openai/v1")+"\nwith an API key.")
	}

	pending := 0
	for _, e := range edits {
		if !e.NoOp {
			pending++
		}
	}
	if !a.JSON {
		a.printPlans(rep.Plans)
	}
	rep.Options = optionsNotes() // also in --json output that makes no change
	if pending == 0 {
		// The gateway guidance is shown only when an agent is actually pointed at it (an edit is
		// in place, either already or from this run). Otherwise setup only says what it did not change.
		gatewayUp := len(edits) > 0
		if gatewayUp {
			rep.Verify, rep.TurnOff = verifySteps(cfg.Gateway.Port), turnOffSteps()
			rep.Explain, rep.Options = explainSteps(dir, cfg), optionsNotes()
		}
		summary := "Nothing to change."
		if p := problemSummary(rep.Plans); p != "" {
			summary = "Nothing to change: " + p
		}
		if !a.JSON {
			fmt.Fprintln(a.Out, summary+"\n")
		}
		statusText, err := statusLineStep(a, &rep, dir, claudeDir, statusLine)
		if err != nil {
			return err
		}
		if a.JSON {
			return a.emit(rep, "")
		}
		fmt.Fprint(a.Out, statusText)
		if gatewayUp {
			fmt.Fprint(a.Out, gatewayGuidance(rep))
		}
		return nil
	}
	consent := yes
	if !consent && !a.JSON {
		consent = a.confirm(fmt.Sprintf("Apply %d change(s)? Originals are backed up and `trove undo` reverses them.", pending))
	}
	if !consent {
		return a.emit(rep, "No changes made. Re-run with --yes (or answer y) to apply.")
	}

	if err := ensureConfigFile(dir); err != nil {
		return err
	}
	applied, err := setup.Apply(dir, edits, time.Now())
	rep.Applied, rep.Changes = len(applied) > 0, applied
	if err != nil {
		_ = a.emit(rep, "")
		return fmt.Errorf("%v. Run `trove undo` to reverse anything already applied", err)
	}
	rep.Verify, rep.TurnOff = verifySteps(cfg.Gateway.Port), turnOffSteps()
	rep.Explain, rep.Options = explainSteps(dir, cfg), optionsNotes()
	// The gateway edit is in place now, so the guidance below is true. Say what was applied first, then
	// offer the optional status line, so the prompt does not come before the result.
	if !a.JSON {
		fmt.Fprintf(a.Out, "Applied %d change(s). Backups are in %s.\n\n", len(rep.Changes), filepath.Join(dir, "backups"))
	}
	statusText, err := statusLineStep(a, &rep, dir, claudeDir, statusLine)
	if err != nil {
		_ = a.emit(rep, "")
		return fmt.Errorf("%v. Run `trove undo` to reverse anything already applied", err)
	}
	if a.JSON {
		return a.emit(rep, "")
	}
	fmt.Fprint(a.Out, statusText)
	fmt.Fprint(a.Out, gatewayGuidance(rep))
	return nil
}

// explainSteps is the closing note of setup: what is recorded, where it stays,
// that Trove starts by only watching, and how to see results.
func explainSteps(dir string, cfg config.Config) []string {
	return []string{
		"What happens next:",
		"  Trove records metadata for each request that goes through the gateway: token counts, sizes, hashes, model names and timings.",
		"  It never stores your prompts, code, file paths, tool output or API keys.",
		"  Everything stays in " + dir + " on this machine. Trove sends nothing anywhere; the only network traffic is your own requests going on to your provider.",
		fmt.Sprintf("  Trove starts in observe-only mode. No optimizer is on and no request is changed. It estimates what each optimizer would save, and offers one only after a baseline of %d days and %d sessions.",
			cfg.Measure.BaselineDays, cfg.Measure.BaselineSessions),
		"  To see results, run `trove` (or `trove report`). Until you have made a few requests through the gateway it will say there is nothing to show yet.",
	}
}

// optionsNotes is the part of setup that says who the gateway is for. The
// sources and dates are in docs/compliance.md (checked 2026-11-04).
func optionsNotes() []string {
	return []string{
		"Who the gateway is for:",
		"  The gateway works with API keys and cloud credentials only: a Console API key, or a Google Vertex, Microsoft Foundry or Amazon Bedrock key.",
		"  If you use Claude Code with a Free, Pro, Max, Ultra, Team or Enterprise login, the gateway refuses it (it never touches subscription logins).",
		"  To use a subscription, run `trove undo` to remove the custom endpoint and use the Trove plugin instead.",
		"",
		"Max, Ultra and Team plans include monthly API credits:",
		"  Link a Claude Console organization to your plan (claude.ai Settings > Billing, or Organization settings > Billing on Team),",
		"  create an API key in that organization, and use it for automated work, such as Agent SDK programs and CI, through the gateway.",
		"  Anthropic's credits page says they cover the Claude API and the Agent SDK, and do not cover interactive Claude Code or the cloud platforms (Bedrock, Vertex, Foundry).",
		"  It does not mention `claude -p`; check your Console billing page before assuming credits apply to it.",
		"  Trove cannot tell automated from interactive work by itself. To label automated runs, add the header X-Trove-Kind: automated",
		"  (Claude Code: ANTHROPIC_CUSTOM_HEADERS=\"X-Trove-Kind: automated\"). `trove report` then shows that spend separately.",
		"",
		"Cloud backends and self-hosted models:",
		"  Vertex and Foundry go through the gateway (set upstreams.vertex or upstreams.foundry). Bedrock works with an Amazon Bedrock API key; AWS SigV4-signed",
		"  requests are refused because the signature covers the request and Trove never holds AWS credentials. OpenAI-compatible servers (vLLM, SGLang, or",
		"  providers of open-weight models) are added under [[upstreams.compat]] for OpenAI-format agents such as Codex CLI; Claude Code is not supported against them.",
		"",
		"Healthcare: Anthropic's HIPAA configuration does not cover Claude Code routed through a gateway. If you rely on a BAA, use the plugin only and do not use this gateway.",
	}
}

// statusLineCommand is what Claude Code will run for its status line. It uses the
// absolute path of this binary so it works without trove on PATH, and passes the
// data directory only if the user chose a non-default one.
func statusLineCommand(a *App, dir string) (string, error) {
	bin, err := troveBinary(a)
	if err != nil {
		return "", err
	}
	cmd := shellQuote(bin) + " status-line --format claude"
	if a.DataDir != "" || a.Getenv("TROVE_DATA_DIR") != "" {
		cmd += " --data-dir " + shellQuote(dir)
	}
	return cmd, nil
}

// statusLineStep offers to add Trove to Claude Code's status line. It runs after
// the gateway change has been applied, plans from the file as it now is, and
// applies only with its own consent: the --status-line flag, or a yes at the
// prompt. --yes alone does not agree to it. It returns text for the summary.
func statusLineStep(a *App, rep *setupReport, dir, claudeDir string, flag bool) (string, error) {
	return statusLineStepRetry(a, rep, dir, claudeDir, flag, "trove setup --status-line")
}

// statusLineStepRetry is statusLineStep with the command named in the "add it
// later" hint, so the plugin's command does not point at the gateway setup.
func statusLineStepRetry(a *App, rep *setupReport, dir, claudeDir string, flag bool, retry string) (string, error) {
	if claudeDir == "" {
		return "", nil
	}
	cmdLine, err := statusLineCommand(a, dir)
	if err != nil {
		return "", err
	}
	e, err := setup.PlanStatusLine(claudeDir, cmdLine)
	var exists *setup.StatusLineExistsError
	switch {
	case errors.As(err, &exists):
		note := "You already have a Claude Code status line, so Trove left it alone. To add Trove's, call `trove status-line --format claude` from your own script (it reads Claude Code's JSON on stdin)."
		rep.StatusLine = &setupPlanJSON{Agent: "Claude Code status line", Path: exists.Path, Note: note}
		return note + "\n\n", nil
	case err != nil:
		rep.StatusLine = &setupPlanJSON{Agent: "Claude Code status line", Problem: err.Error()}
		return "Status line not added: " + err.Error() + "\n\n", nil
	}
	rep.StatusLine = &setupPlanJSON{Agent: "Claude Code status line", Path: e.Path, Summary: e.Summary, Adds: e.Show, Note: e.Note, AlreadyDone: e.NoOp}
	if e.NoOp {
		return "Claude Code's status line already shows Trove.\n\n", nil
	}
	if !a.JSON {
		a.printPlans([]setupPlanJSON{*rep.StatusLine})
	}
	consent := flag
	if !consent && !a.JSON {
		consent = a.confirm("Add Trove to Claude Code's status line? Your settings file is backed up first and `trove undo` removes it.")
	}
	if !consent {
		return "Status line not added. Add it later with `" + retry + "`.\n\n", nil
	}
	if err := ensureConfigFile(dir); err != nil {
		return "", err
	}
	applied, err := setup.Apply(dir, []*setup.Edit{e}, time.Now())
	rep.Changes = append(rep.Changes, applied...)
	if err != nil {
		return "", err
	}
	rep.StatusLineApplied = len(applied) > 0
	return "Added Trove to Claude Code's status line. It appears after your next reply.\n\n", nil
}

// problemSummary lists what stopped setup from pointing an agent at the gateway, or "" when nothing did.
// An optional, not-requested gateway is not a problem and is not listed.
func problemSummary(ps []setupPlanJSON) string {
	var parts []string
	for _, p := range ps {
		switch {
		case p.Problem != "":
			parts = append(parts, p.Agent+": "+p.Problem)
		case p.AlreadyDone:
			parts = append(parts, p.Agent+" is already pointed at the gateway")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + "."
}

// gatewayGuidance is the verify, turn-off and explanation text. It is shown only once the gateway is set up.
func gatewayGuidance(rep setupReport) string {
	return "\n" + strings.Join(rep.Verify, "\n") + "\n\n" + strings.Join(rep.TurnOff, "\n") + "\n\n" +
		strings.Join(rep.Explain, "\n") + "\n\n" + strings.Join(rep.Options, "\n") + "\n"
}

func (a *App) printPlans(ps []setupPlanJSON) {
	for _, p := range ps {
		_, _ = fmt.Fprintf(a.Out, "%s\n", a.paint("1", p.Agent))
		switch {
		case p.Problem != "":
			_, _ = fmt.Fprintf(a.Out, "  cannot set up: %s\n\n", p.Problem)
		case p.Optional != "":
			_, _ = fmt.Fprintf(a.Out, "  %s\n\n", p.Optional)
		case p.AlreadyDone:
			_, _ = fmt.Fprintf(a.Out, "  already pointed at the gateway (%s)\n\n", p.Path)
		default:
			_, _ = fmt.Fprintf(a.Out, "  %s\n  file: %s\n  adds:\n", p.Summary, p.Path)
			for _, l := range strings.Split(strings.TrimRight(p.Adds, "\n"), "\n") {
				_, _ = fmt.Fprintf(a.Out, "      %s\n", l)
			}
			_, _ = fmt.Fprintf(a.Out, "  note: %s\n\n", p.Note)
		}
	}
}

func ensureConfigFile(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %v", dir, err)
	}
	p := filepath.Join(dir, config.FileName)
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	if err := os.WriteFile(p, []byte(config.DefaultTOML), 0o600); err != nil {
		return fmt.Errorf("cannot write %s: %v", p, err)
	}
	return nil
}

func verifySteps(port int) []string {
	return []string{
		"To verify it works:",
		"  1. Start the gateway:   trove gateway start        (leave it running, or use --background)",
		fmt.Sprintf("  2. Check it responds:   curl http://127.0.0.1:%d/trove/health", port),
		"  3. Claude Code: with ANTHROPIC_API_KEY set, run `claude`, then /status: it should show",
		fmt.Sprintf("     \"Anthropic base URL: http://127.0.0.1:%d/anthropic\". Send a message.", port),
		"     Codex CLI: with OPENAI_API_KEY set, run `codex` and send a message.",
		"  4. Run `trove gateway status`: the request count should have gone up.",
	}
}

func turnOffSteps() []string {
	return []string{
		"To turn it off:",
		"  trove undo             restores the files Trove changed, exactly as they were",
		"  trove gateway stop     stops the gateway and puts back the gateway setting it changed (agents go straight to their providers)",
	}
}

func newUndo(a *App) *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:   "undo [id...]",
		Short: "Reverse changes made by `trove setup` and `trove fix`",
		Long: "With one or more ids (a fix id from `trove fix`, or a change id), reverses just those changes.\n" +
			"With no id, or with --all, reverses everything Trove changed, newest first. Files you have not edited since are restored byte for byte.",
		RunE: func(_ *cobra.Command, ids []string) error {
			if all && len(ids) > 0 {
				return errors.New("give either --all or specific ids, not both")
			}
			dir, err := a.ResolveDataDir()
			if err != nil {
				return err
			}
			res, err := setup.UndoSelected(dir, ids)
			if err != nil {
				return err
			}
			if err := fixer.MarkReverted(dir, res, time.Now()); err != nil {
				_, _ = fmt.Fprintf(a.Err, "Note: the files were restored, but Trove could not update its record of the fixes (%v).\n", err)
			}
			if len(res) == 0 {
				return a.emit(map[string]any{"undone": []setup.UndoResult{}}, "Nothing to undo: Trove has not changed any agent settings.")
			}
			var b strings.Builder
			for _, r := range res {
				fmt.Fprintf(&b, "%s: %s (%s)\n", r.Agent, r.Detail, r.Path)
			}
			if len(ids) == 0 {
				b.WriteString("Your agents talk to their providers directly again. Stop the gateway with `trove gateway stop` if it is running.")
			}
			return a.emit(map[string]any{"undone": res}, strings.TrimRight(b.String(), "\n"))
		},
	}
	c.Flags().BoolVar(&all, "all", false, "reverse everything (the default when no id is given)")
	return c
}
