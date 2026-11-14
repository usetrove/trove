package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// everydayCommands is what `trove --help` shows. Everything else still works and
// keeps its name (the plugin, the hooks, CI and scripts call many of them), but is
// hidden from the main help so a new user sees a few commands, not thirty.
// `trove advanced` lists the rest.
var everydayCommands = map[string]bool{
	"setup":     true, // find agents, offer Autopilot
	"autopilot": true, // on, off, pause, resume, or see what it is doing
	"report":    true, // also what plain `trove` prints
	"fix":       true, // config fixes with a diff and undo
	"undo":      true,
	"doctor":    true,
	"uninstall": true,
	"advanced":  true,
}

// machineCommands are run by agents, the plugin or the shell, not by people, so
// `trove advanced` does not list them. They still work.
var machineCommands = map[string]bool{"hook": true, "engine": true, "status-line": true, "version": true, "completion": true}

type advancedCommandJSON struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// hideAdvanced marks every command outside everydayCommands as hidden and adds
// `trove advanced`. Hidden commands run exactly as before.
func hideAdvanced(a *App, root *cobra.Command) {
	root.CompletionOptions.HiddenDefaultCmd = true
	root.AddCommand(&cobra.Command{
		Use:   "advanced",
		Short: "List the other commands (most people never need them)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			var list []advancedCommandJSON
			for _, sub := range root.Commands() {
				if sub.Hidden && sub.Name() != "help" && !machineCommands[sub.Name()] {
					list = append(list, advancedCommandJSON{sub.Name(), sub.Short})
				}
			}
			sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
			var b strings.Builder
			b.WriteString("Other commands. Autopilot and the plugin do most of this for you.\n\n")
			for _, c := range list {
				fmt.Fprintf(&b, "  %-14s %s\n", c.Name, c.Description)
			}
			b.WriteString("\nRun `trove <command> --help` for details.")
			return a.emit(list, b.String())
		},
	})
	for _, sub := range root.Commands() {
		if !everydayCommands[sub.Name()] && sub.Name() != "help" {
			sub.Hidden = true
		}
	}
}
