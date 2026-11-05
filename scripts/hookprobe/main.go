// Command hookprobe checks, on the installed Claude Code, which tool calls a
// hook can really rewrite, and writes the result as JSON. Keep it in the repo and
// rerun it on every new Claude Code release:
//
//	go run ./scripts/hookprobe                                  # version + what the docs say; runs nothing
//	go run ./scripts/hookprobe --live --max-usd 0.25            # real sessions; costs a few cents
//	go run ./scripts/hookprobe --live --max-usd 0.25 --record-dir internal/hooks/testdata/recorded
//
// It runs your own `claude` with your own login and never reads credentials. The
// same code backs `trove hooks probe`; this is the version that needs no built
// trove binary.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/usetrove/trove/internal/hooks/probe"
)

func main() {
	// The agent runs this same binary as the probe hook: `hookprobe __hook pre|post`.
	if len(os.Args) == 3 && os.Args[1] == "__hook" {
		os.Exit(probe.HookMain(os.Args[2], os.Stdin, os.Stdout, os.Getenv))
	}
	live := flag.Bool("live", false, "run real Claude Code sessions (costs money; needs -max-usd)")
	maxUSD := flag.Float64("max-usd", 0, "spending cap in USD for a live run, split across tools")
	model := flag.String("model", "claude-haiku-5-5", "model for the probe sessions")
	claude := flag.String("claude", "claude", "path of the Claude Code binary")
	tools := flag.String("tools", strings.Join(probe.DefaultTools, ","), "comma-separated tools to probe")
	out := flag.String("out", "docs/hook-capabilities.json", "where to write the JSON result ('-' for stdout, '' for none)")
	record := flag.String("record-dir", "", "save redacted copies of the hook inputs the agent sent (test fixtures)")
	flag.Parse()

	self, err := os.Executable()
	if err != nil {
		fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	rep, err := probe.Run(ctx, probe.Options{
		Claude: *claude, Live: *live, MaxUSD: *maxUSD, Model: *model, Tools: strings.Split(*tools, ","), RecordDir: *record,
		HookCommand: func(mode string) string { return fmt.Sprintf("%q __hook %s", self, mode) },
	})
	if err != nil {
		fail(err)
	}
	b, _ := json.MarshalIndent(rep, "", "  ")
	switch *out {
	case "":
	case "-":
		fmt.Println(string(b))
	default:
		if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil { //nolint:gosec // a committed report
			fail(err)
		}
	}
	mode := "docs only (nothing was run)"
	if rep.Live {
		mode = fmt.Sprintf("live, about $%.2f", rep.SpentUSD)
	}
	fmt.Fprintf(os.Stderr, "Claude Code %s, %s\n", rep.AgentVersion, mode)
	for _, e := range rep.Capabilities.Entries {
		fmt.Fprintf(os.Stderr, "  %-7s %-5s input=%-8s output=%-8s [%s] %s\n", e.Agent, e.Tool, e.InputRewrite, e.OutputRewrite, e.Method, e.Evidence)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "hookprobe:", err)
	os.Exit(1)
}
