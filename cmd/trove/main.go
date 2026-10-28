// Command trove is the Trove CLI.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/jaclynsunardi/trove/internal/cli"
	"github.com/jaclynsunardi/trove/internal/redact"
)

func main() {
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
