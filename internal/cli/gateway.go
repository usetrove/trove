package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/usetrove/trove/internal/mcp/install"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/analyze"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/gateway"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/service"
	"github.com/usetrove/trove/internal/store"
)

// pidFile records the running gateway so stop and status can find it.
type pidFile struct {
	PID  int    `json:"pid"`
	Addr string `json:"addr"`
}

const pidFileName = "gateway.json"

func readPID(dir string) (pidFile, bool) {
	b, err := os.ReadFile(filepath.Join(dir, pidFileName)) //nolint:gosec // inside the data dir
	if err != nil {
		return pidFile{}, false
	}
	var p pidFile
	if json.Unmarshal(b, &p) != nil || p.PID == 0 || p.Addr == "" {
		return pidFile{}, false
	}
	return p, true
}

type health struct {
	Status   string `json:"status"`
	Version  string `json:"version"`
	Started  string `json:"started"`
	Requests uint64 `json:"requests"`
	Rejected uint64 `json:"rejected"`
}

// probe asks a gateway at addr for its health. It only ever talks to loopback.
func probe(addr string) (health, bool) {
	if gateway.ValidateListenAddr(addr) != nil {
		return health{}, false
	}
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + addr + gateway.HealthPath)
	if err != nil {
		return health{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	var h health
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&h) != nil || h.Status != "ok" {
		return health{}, false
	}
	return h, true
}

func newGateway(a *App) *cobra.Command {
	g := &cobra.Command{Use: "gateway", Short: "Run the opt-in local gateway", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return c.Help() }}
	g.AddCommand(newGatewayStart(a), newGatewayStop(a), newGatewayStatus(a))
	return g
}

func newGatewayStart(a *App) *cobra.Command {
	var listen string
	var background, yes bool
	c := &cobra.Command{
		Use:   "start",
		Short: "Start the local gateway (foreground; --background installs a user service)",
		Long: "Runs the gateway on 127.0.0.1 in the foreground until you press Ctrl-C.\n" +
			"With --background, Trove shows you the exact launchd (macOS) or systemd (Linux) user service it would install and asks first.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if background {
				return startBackground(a, yes)
			}
			return runGateway(a, listen)
		},
	}
	c.Flags().StringVar(&listen, "listen", "", "address to listen on; must be loopback (default 127.0.0.1:<gateway.port>)")
	c.Flags().BoolVar(&background, "background", false, "install and start a user-level service instead of running in the foreground")
	c.Flags().BoolVar(&yes, "yes", false, "agree to installing the background service without asking")
	return c
}

func loadConfig(a *App) (string, config.Config, error) {
	dir, err := a.ResolveDataDir()
	if err != nil {
		return "", config.Config{}, err
	}
	cfg, err := config.Load(dir, a.Getenv)
	return dir, cfg, err
}

func runGateway(a *App, listen string) error {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	if listen == "" {
		listen = fmt.Sprintf("127.0.0.1:%d", cfg.Gateway.Port)
	}
	if err := gateway.ValidateListenAddr(listen); err != nil {
		return err
	}
	if p, ok := readPID(dir); ok {
		if _, up := probe(p.Addr); up {
			return fmt.Errorf("the gateway is already running at %s (pid %d). Use `trove gateway status`, or `trove gateway stop` first", p.Addr, p.PID)
		}
	}
	db, err := store.Open(dir)
	if err != nil {
		return fmt.Errorf("cannot open Trove's database: %v. Run `trove doctor` for details", err)
	}
	defer func() { _ = db.Close() }()

	log := a.Logger()
	writer := store.NewWriter(db, store.WriterOptions{Logger: log})
	tbl, err := pricing.Default()
	if err != nil {
		log.Warn("pricing data could not be loaded; costs will be recorded as unknown", "err", err)
		tbl = nil
	}
	opts := optimize.Options{
		Config: cfg, Pins: db.Pins(), KillFile: optimize.KillFile(dir), Getenv: a.Getenv, Log: log,
		Gate: measure.NewGate(db, cfg.Measure, nil).State,
	}
	if install.Registered(dir) {
		// The output trimmer may be on only if trimmed output can be stored and
		// fetched back with the Trove MCP server.
		if rec, _, err := openRecovery(a); err != nil {
			log.Warn("the recovery store could not be opened, so the output trimmer stays in shadow mode", "err", err)
		} else {
			opts.Recovery = rec
		}
	}
	engine := optimize.New(opts)
	srv, err := gateway.New(gateway.Options{Config: cfg, Recorder: writer, Pricing: tbl, Analyzer: analyze.New(analyze.Options{Pricing: tbl, Logger: log}), Optimizer: engine, Logger: log, Version: Version})
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %v. Another process may be using that port; change gateway.port in %s or pass --listen", listen, err, filepath.Join(dir, config.FileName))
	}

	base := a.Ctx
	if base == nil {
		base = context.Background()
	}
	ctx, stop := signal.NotifyContext(base, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go measure.Run(ctx, db, func() config.Config { return cfg }, time.Duration(cfg.Gateway.IdleMinutes)*time.Minute, 5*time.Minute,
		func(err error) { log.Warn("could not update measurements; requests are unaffected", "err", err) }, notifier())
	go db.RunRetention(ctx, func() int { return cfg.RetentionDays }, 24*time.Hour, func(err error) { log.Warn("could not delete old usage rows", "err", err) })

	pid, _ := json.Marshal(pidFile{PID: os.Getpid(), Addr: ln.Addr().String()})
	pidPath := filepath.Join(dir, pidFileName)
	if err := os.WriteFile(pidPath, pid, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("cannot write %s: %v", pidPath, err)
	}
	defer func() { _ = os.Remove(pidPath) }()

	_, _ = fmt.Fprintf(a.Err, "Trove gateway listening on http://%s (loopback only). Press Ctrl-C to stop.\n", ln.Addr())
	_, _ = fmt.Fprintf(a.Err, "  Anthropic: %s   OpenAI: %s\n", setupURL(ln.Addr().String(), "/anthropic"), setupURL(ln.Addr().String(), "/openai/v1"))
	_, _ = fmt.Fprintln(a.Err, "  Point your agents at it with `trove setup`. Only API-key authentication is accepted.")

	serveErr := srv.Serve(ctx, ln)
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := writer.Close(cctx); err != nil {
		log.Warn("some usage rows may not have been saved before exit", "err", err)
	}
	if st := writer.Stats(); st.Dropped+st.Failed > 0 {
		_, _ = fmt.Fprintf(a.Err, "Note: %d usage rows were dropped and %d failed to save; requests were not affected.\n", st.Dropped, st.Failed)
	}
	return serveErr
}

func setupURL(addr, route string) string { return "http://" + addr + route }

func startBackground(a *App, yes bool) error {
	dir, _, err := loadConfig(a)
	if err != nil {
		return err
	}
	home, err := a.HomeDir()
	if err != nil {
		return errors.New("cannot find your home directory; set HOME and try again")
	}
	exe := ""
	if a.Executable != nil {
		exe, err = a.Executable()
		if err != nil {
			return fmt.Errorf("cannot find the trove binary path: %v", err)
		}
	}
	plan, err := service.Build(runtime.GOOS, home, os.Getuid(), exe, []string{"gateway", "start", "--data-dir", dir})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(a.Out, "Trove would install a user-level service (no admin rights, runs as you):\n\n  file: %s\n\n%s\n  then run:\n", plan.UnitPath, plan.Unit)
	for _, c := range plan.Install {
		_, _ = fmt.Fprintf(a.Out, "    %s\n", join(c))
	}
	canAsk := a.In != nil && !a.JSON
	if !yes && (!canAsk || !a.confirm("Install and start this service?")) {
		return errors.New("nothing was installed. Re-run with --yes to agree, or run `trove gateway start` in a terminal instead")
	}
	if err := os.MkdirAll(filepath.Dir(plan.UnitPath), 0o755); err != nil { //nolint:gosec // user service dirs are conventionally 0755
		return fmt.Errorf("cannot create %s: %v", filepath.Dir(plan.UnitPath), err)
	}
	if err := os.WriteFile(plan.UnitPath, []byte(plan.Unit), 0o644); err != nil { //nolint:gosec // service definitions are not secret
		return fmt.Errorf("cannot write %s: %v", plan.UnitPath, err)
	}
	for _, c := range plan.Install {
		if err := a.Run(c[0], c[1:]...); err != nil {
			return fmt.Errorf("%v. The service file was written to %s; remove it with `trove gateway stop --remove-service`", err, plan.UnitPath)
		}
	}
	return a.emit(map[string]any{"installed": true, "unit": plan.UnitPath},
		"Installed and started. Check it with `trove gateway status`; remove it with `trove gateway stop --remove-service`.")
}

func join(c []string) string {
	s := ""
	for i, p := range c {
		if i > 0 {
			s += " "
		}
		s += p
	}
	return s
}

type statusReport struct {
	Running  bool   `json:"running"`
	PID      int    `json:"pid,omitempty"`
	Addr     string `json:"addr,omitempty"`
	Version  string `json:"version,omitempty"`
	Started  string `json:"started,omitempty"`
	Requests uint64 `json:"requests_since_start,omitempty"`
	Rejected uint64 `json:"rejected_non_api_key,omitempty"`
}

func newGatewayStatus(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the gateway is running",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, err := a.ResolveDataDir()
			if err != nil {
				return err
			}
			p, ok := readPID(dir)
			if !ok {
				return a.emit(statusReport{}, "The gateway is not running. Start it with `trove gateway start`.")
			}
			h, up := probe(p.Addr)
			if !up {
				_ = os.Remove(filepath.Join(dir, pidFileName))
				return a.emit(statusReport{}, "The gateway is not running (a stale record was cleaned up). Start it with `trove gateway start`.")
			}
			r := statusReport{Running: true, PID: p.PID, Addr: p.Addr, Version: h.Version, Started: h.Started, Requests: h.Requests, Rejected: h.Rejected}
			return a.emit(r, fmt.Sprintf("The gateway is running at http://%s (pid %d, trove %s, started %s).\n%d requests handled since start; %d refused because they were not API-key requests.",
				p.Addr, p.PID, h.Version, h.Started, h.Requests, h.Rejected))
		},
	}
}

func newGatewayStop(a *App) *cobra.Command {
	var removeService bool
	c := &cobra.Command{
		Use:   "stop",
		Short: "Stop the local gateway",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, err := a.ResolveDataDir()
			if err != nil {
				return err
			}
			notes := stopService(a, removeService)
			p, ok := readPID(dir)
			if !ok {
				return a.emit(map[string]any{"stopped": false, "notes": notes}, joinLines(append([]string{"The gateway is not running."}, notes...)))
			}
			if _, up := probe(p.Addr); !up {
				_ = os.Remove(filepath.Join(dir, pidFileName))
				return a.emit(map[string]any{"stopped": false, "notes": notes}, joinLines(append([]string{"The gateway was not running (stale record cleaned up)."}, notes...)))
			}
			proc, err := os.FindProcess(p.PID)
			if err != nil {
				return fmt.Errorf("cannot find the gateway process %d: %v", p.PID, err)
			}
			if err := proc.Signal(syscall.SIGTERM); err != nil {
				if err := proc.Kill(); err != nil { // Windows has no SIGTERM
					return fmt.Errorf("cannot stop pid %d: %v. Close the terminal running `trove gateway start`", p.PID, err)
				}
			}
			for i := 0; i < 50; i++ {
				if _, up := probe(p.Addr); !up {
					_ = os.Remove(filepath.Join(dir, pidFileName))
					return a.emit(map[string]any{"stopped": true, "notes": notes}, joinLines(append([]string{"The gateway has stopped."}, notes...)))
				}
				time.Sleep(100 * time.Millisecond)
			}
			return errors.New("the gateway did not stop within 5 seconds; close the terminal running it, or end pid " + fmt.Sprint(p.PID))
		},
	}
	c.Flags().BoolVar(&removeService, "remove-service", false, "also remove the background service definition")
	return c
}

func joinLines(l []string) string {
	s := ""
	for i, x := range l {
		if i > 0 {
			s += "\n"
		}
		s += x
	}
	return s
}

// stopService unloads the background service if one is installed, so the OS
// does not restart the gateway right after it is stopped.
func stopService(a *App, remove bool) []string {
	home, err := a.HomeDir()
	if err != nil || a.Run == nil {
		return nil
	}
	plan, err := service.Current(home, os.Getuid(), "/trove", nil)
	if err != nil {
		return nil
	}
	if _, err := os.Stat(plan.UnitPath); err != nil {
		return nil
	}
	var notes []string
	for _, c := range plan.Remove {
		if err := a.Run(c[0], c[1:]...); err != nil {
			notes = append(notes, "Could not stop the background service: "+err.Error())
		}
	}
	if remove {
		if err := os.Remove(plan.UnitPath); err != nil {
			notes = append(notes, "Could not remove "+plan.UnitPath+": "+err.Error())
		} else {
			notes = append(notes, "Removed the background service definition "+plan.UnitPath+".")
		}
	} else {
		notes = append(notes, "The background service is stopped but still installed (it starts again at next login). Remove it with `trove gateway stop --remove-service`.")
	}
	return notes
}
