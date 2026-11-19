package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/gateway"
)

// probeOutput is the --json shape of `trove budget probe`.
type probeOutput struct {
	Results []gateway.ProbeRecord `json:"results"`
	Message string                `json:"message"`
}

func newBudgetProbe(a *App) *cobra.Command {
	var port int
	var wait, quiet time.Duration
	c := &cobra.Command{
		Use:   "probe",
		Short: "Check how your agent handles a spend-cap refusal (needed before the cap may refuse it)",
		Long: "Starts a fake provider on 127.0.0.1 that answers every request with exactly the refusal the spend cap\n" +
			"sends, and counts how often each agent tries. It forwards nothing and needs no API key. Point your agent at\n" +
			"it in another terminal (see docs/spend-cap.md), then answer whether the agent showed Trove's message.\n" +
			"An agent that tried once and showed the message passes; only then may spend_cap = \"on\" refuse it.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return budgetProbe(a, port, wait, quiet) },
	}
	c.Flags().IntVar(&port, "port", 8799, "loopback port for the fake provider")
	c.Flags().DurationVar(&wait, "wait", 5*time.Minute, "how long to wait for the agent's first request")
	c.Flags().DurationVar(&quiet, "quiet", 30*time.Second, "how long to keep counting after the last request")
	return c
}

func budgetProbe(a *App, port int, wait, quiet time.Duration) error {
	// The verdict rests on a person saying what the agent showed, so there is no --yes.
	if err := needTerminal("recording a spend-cap probe result"); err != nil {
		return err
	}
	dir, _, err := loadConfig(a)
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %v. Pick another port with --port", addr, err)
	}
	p := gateway.NewRefusalProbe(time.Now)
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	base := "http://" + addr
	_, _ = fmt.Fprintf(a.Err, "Fake provider listening on %s. Nothing is forwarded.\n"+
		"In another terminal, run your agent against it with a placeholder key, for example:\n"+
		"  ANTHROPIC_BASE_URL=%s%s ANTHROPIC_API_KEY=trove-probe-placeholder claude -p \"say hi\"\n"+
		"For other agents, use the base URL %s%s or %s%s/v1 the way `trove setup` does, on this port.\n"+
		"Waiting up to %s for the first request; Ctrl-C stops early.\n",
		addr, base, gateway.PrefixAnthropic, base, gateway.PrefixAnthropic, base, gateway.PrefixOpenAI, wait)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	waitForProbe(ctx, p, wait, quiet)

	seen := p.Seen()
	if len(seen) == 0 {
		return errors.New("no agent reached the probe, so nothing was recorded. Check the base URL and run this again")
	}
	var out probeOutput
	var lines []string
	for _, s := range seen {
		shown := a.confirm(fmt.Sprintf("%s (%s) tried %d time(s). Did it show Trove's spend cap message and stop?", s.Agent, versionOrUnknown(s.AgentVersion), s.Attempts))
		rec := gateway.ProbeRecord{Agent: s.Agent, AgentVersion: s.AgentVersion, Attempts: s.Attempts,
			Verdict: gateway.ProbeVerdict(s.Attempts, shown), CheckedAt: time.Now().UTC()}
		if err := gateway.SaveProbe(dir, rec); err != nil {
			return err
		}
		out.Results = append(out.Results, rec)
		lines = append(lines, fmt.Sprintf("%s: %s", s.Agent, verdictWords(rec.Verdict)))
	}
	out.Message = strings.Join(lines, "\n") + "\nRestart the gateway for this to take effect."
	return a.emit(out, out.Message)
}

// waitForProbe returns when ctx ends, when no request came within wait, or when quiet has
// passed since the last request.
func waitForProbe(ctx context.Context, p *gateway.RefusalProbe, wait, quiet time.Duration) {
	start := time.Now()
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		last := p.LastRequest()
		if last.IsZero() && time.Since(start) >= wait || !last.IsZero() && time.Since(last) >= quiet {
			return
		}
	}
}

func verdictWords(v string) string {
	switch v {
	case gateway.VerdictStops:
		return "passed: it tried once and showed the message. With spend_cap = \"on\", the cap may now refuse it."
	case gateway.VerdictRetries:
		return "failed: it sent the request again, so the cap will only warn for it."
	case gateway.VerdictNotShown:
		return "failed: the message was not shown, so the cap will only warn for it."
	}
	return "no request seen."
}

func versionOrUnknown(s string) string {
	if s == "" {
		return "version unknown"
	}
	return s
}

// capVerified names the agents the spend cap may refuse, for `trove optimize status`.
func capVerified(dir string) string {
	recs, err := gateway.LoadProbes(dir)
	if err != nil {
		return "the probe results cannot be read, so no agent is refused"
	}
	var ok []string
	for _, r := range recs {
		if r.Verdict == gateway.VerdictStops {
			ok = append(ok, r.Agent)
		}
	}
	if len(ok) == 0 {
		return "no agent has passed yet, so it only warns"
	}
	return "passed: " + strings.Join(ok, ", ")
}
