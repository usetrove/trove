package cli

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/adapter/plugin"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/experiment"
	"github.com/usetrove/trove/internal/hooks"
	"github.com/usetrove/trove/internal/mcp/install"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/store"
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
			engOpts := optimize.Options{Config: cfg, KillFile: optimize.KillFile(dir), Getenv: a.Getenv}
			// As in the gateway: the output trimmer may be "on" only when trimmed text
			// can be stored and fetched back with the Trove MCP server.
			recoverable := false
			if install.Registered(dir) {
				if rec, _, rerr := openRecovery(a); rerr != nil {
					a.Logger().Warn("the recovery store could not be opened, so the output trimmer stays in shadow mode", "err", rerr)
				} else {
					engOpts.Recovery, recoverable = rec, true
				}
			}
			eng := optimize.New(engOpts)
			opts := plugin.Options{Version: Version, Recoverable: recoverable}
			// The store is optional: without it nothing is recorded, rollbacks cannot be
			// read, and so no optimizer may be "on" (fail safe, never fail loud).
			db, derr := store.Open(dir)
			if derr != nil {
				db = nil
				a.Logger().Warn("Trove's database could not be opened, so optimizers run in shadow mode and nothing is recorded", "err", derr)
			}
			if db != nil {
				defer func() { _ = db.Close() }()
				rb := newRollbackCache(db)
				opts.RolledBack = rb.get
				opts.Resolver = pluginResolver(cfg, db, rb, recoverable)
				b := plugin.NewBatcher(func(evs []plugin.Event) error {
					return db.AdapterEvents().Append(context.Background(), toStoreEvents(evs))
				})
				defer b.Close()
				opts.Recorder = b
			} else {
				opts.Resolver = pluginResolver(cfg, nil, nil, recoverable)
			}
			in := a.In
			if in == nil {
				in = os.Stdin
			}
			return plugin.New(eng, opts).Serve(c.Context(), in, a.Out)
		},
	})
	return c
}

// pluginStateNames are the optimizers a plugin can reach: the request-level
// pipeline and the tool-level hooks' optimizers, each once.
func pluginStateNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range append(append([]string{}, optimize.PipelineNames...), hooks.OptimizerNames...) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// pluginResolver pins a session's states the way the other adapters do: the
// configured state, clamped by the engine's own rules, lowered to shadow by a
// guardrail rollback and by the control arm of an A/B experiment. Without a
// database the rollbacks cannot be read, so "on" becomes "shadow".
func pluginResolver(cfg config.Config, db *store.DB, rb *rollbackCache, recoverable bool) plugin.Resolver {
	var assigner *experiment.Assigner
	if db != nil && cfg.Experiment.Enabled {
		assigner = experiment.NewAssigner(db, cfg.Experiment, nil, nil)
	}
	return func(key string) map[string]string {
		rolled := map[string]bool{}
		if rb != nil {
			rolled = rb.get()
		}
		states := map[string]string{}
		for _, n := range pluginStateNames() {
			s := optimize.EffectiveStateFor(n, cfg.OptimizerState(n), recoverable)
			if s == config.StateOn {
				switch {
				case db == nil, rolled[n]:
					s = config.StateShadow
				case assigner != nil && assigner.Arm(key, n) != config.StateOn:
					s = config.StateShadow
				}
			}
			states[n] = s
		}
		return states
	}
}

// rollbackCache reads the guardrail rollbacks at most every 30 seconds.
type rollbackCache struct {
	db *store.DB

	mu     sync.Mutex
	at     time.Time
	rolled map[string]bool
}

func newRollbackCache(db *store.DB) *rollbackCache { return &rollbackCache{db: db} }

func (r *rollbackCache) get() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.at.IsZero() && time.Since(r.at) < 30*time.Second {
		return r.rolled
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	m, err := measure.Overrides(ctx, r.db)
	if err != nil {
		// Unknown: keep the last answer, and retry on the next call.
		return r.rolled
	}
	r.rolled = make(map[string]bool, len(m))
	for k := range m {
		r.rolled[k] = true
	}
	r.at = time.Now()
	return r.rolled
}

func toStoreEvents(evs []plugin.Event) []store.AdapterEvent {
	out := make([]store.AdapterEvent, len(evs))
	for i, e := range evs {
		out[i] = store.AdapterEvent{TS: e.TS, SessionKey: e.SessionKey, Harness: e.Harness, Login: e.Login, Provider: e.Provider, Model: e.Model,
			Kind: e.Kind, Optimizer: e.Optimizer, State: e.State, Bytes: e.Bytes, Tokens: e.Tokens, OverheadTokens: e.OverheadTokens,
			InTokens: e.InTokens, OutTokens: e.OutTokens, CacheReadTokens: e.CacheReadTokens, CacheWriteTokens: e.CacheWriteTokens,
			CostUSD: e.CostUSD, Detail: e.Detail}
	}
	return out
}
