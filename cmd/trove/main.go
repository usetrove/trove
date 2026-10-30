// Command trove is the Trove CLI.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/usetrove/trove/internal/cli"
	"github.com/usetrove/trove/internal/redact"
)

func main() {
	// `trove hook <event>` runs on every tool call, so it skips building the
	// command tree. It always exits 0: exit code 2 would block the agent's tool
	// call, so a panic here must not reach the handler below.
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		os.Exit(runHook(os.Args[2:]))
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprint(os.Stderr, redact.FormatPanic(r, debug.Stack()))
			os.Exit(2)
		}
	}()
	app := cli.NewApp()
	app.Err = redact.Writer(os.Stderr)
	os.Exit(cli.Execute(app, os.Args[1:]))
}

func runHook(args []string) (code int) {
	defer func() {
		if recover() != nil {
			code = 0
		}
	}()
	return cli.RunHook(args, os.Stdin, os.Stdout)
}
