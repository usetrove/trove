package cli

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/adapter/plugin"
	"github.com/usetrove/trove/internal/optimize"
)

func newEngine(a *App) *cobra.Command {
	c := &cobra.Command{
		Use:   "engine",
		Short: "Run the optimizer engine for harness plugins",
	}
	c.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "Serve the optimizer engine to a plugin over stdin and stdout (JSON lines)",
		Long: "Runs the optimizer engine as a long-lived child process for harness plugins that are not written in Go.\n" +
			"The plugin sends one JSON message per line on stdin and reads one reply per line on stdout. The protocol\n" +
			"has no place for headers or keys. If a reply says ok is false, the plugin must use its original input.\n" +
			"Protocol: docs/adapters.md. Stdout carries only protocol lines; logs go to stderr.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if stdinIsTerminal() {
				return errors.New("`trove engine serve` talks to a plugin over stdin and stdout, so it does nothing from a terminal. " +
					"See docs/adapters.md for the protocol, or run `trove doctor --capabilities` to see what it can do")
			}
			dir, cfg, err := loadConfig(a)
			if err != nil {
				return err
			}
			eng := optimize.New(optimize.Options{Config: cfg, KillFile: optimize.KillFile(dir), Getenv: a.Getenv})
			in := a.In
			if in == nil {
				in = os.Stdin
			}
			return plugin.New(eng).Serve(c.Context(), in, a.Out)
		},
	})
	return c
}
