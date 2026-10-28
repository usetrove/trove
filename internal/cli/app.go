// Package cli defines Trove's command-line interface.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/redact"
)

// App carries the shared state and injectable dependencies for all commands.
type App struct {
	Out, Err io.Writer
	Getenv   func(string) string
	LookPath func(string) (string, error)
	HomeDir  func() (string, error)

	// In is where consent prompts are read from; nil means "no terminal", so
	// anything needing consent requires --yes.
	In io.Reader
	// Ctx ends the long-running gateway; nil means run until interrupted.
	Ctx context.Context
	// Run executes an external command (service managers). Injected in tests.
	Run func(name string, args ...string) error
	// Executable returns the path of the running trove binary.
	Executable func() (string, error)

	// Global flags.
	JSON    bool
	NoColor bool
	DataDir string
	Verbose bool
}

// NewApp returns an App wired to the real process environment.
func NewApp() *App {
	return &App{
		Out: os.Stdout, Err: os.Stderr, In: os.Stdin, Getenv: os.Getenv, LookPath: exec.LookPath, HomeDir: os.UserHomeDir,
		Executable: os.Executable,
		Run: func(name string, args ...string) error {
			out, err := exec.Command(name, args...).CombinedOutput() //nolint:gosec // fixed service-manager commands shown to the user before they run
			if err != nil {
				return fmt.Errorf("%s %s failed: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			}
			return nil
		},
	}
}

// Logger returns a redacting logger on stderr.
func (a *App) Logger() *slog.Logger { return redact.NewLogger(a.Err, a.Verbose) }

// ResolveDataDir applies --data-dir, TROVE_DATA_DIR, then ~/.trove.
func (a *App) ResolveDataDir() (string, error) { return config.ResolveDataDir(a.DataDir, a.Getenv) }

// Color reports whether output may use ANSI color.
func (a *App) Color() bool {
	if a.NoColor || a.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := a.Out.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// emit writes v as JSON when --json is set, otherwise the human text.
func (a *App) emit(v any, text string) error {
	if a.JSON {
		enc := json.NewEncoder(a.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	_, err := fmt.Fprintln(a.Out, text)
	return err
}

// paint wraps s in an ANSI color code when color is enabled.
func (a *App) paint(code, s string) string {
	if !a.Color() {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// confirm asks a yes/no question. It returns false when there is no terminal
// input, so nothing is ever applied without an explicit yes.
func (a *App) confirm(question string) bool {
	if a.In == nil {
		return false
	}
	_, _ = fmt.Fprintf(a.Out, "%s [y/N] ", question)
	line, _ := bufio.NewReader(a.In).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
