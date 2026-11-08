package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/contribute"
	"github.com/usetrove/trove/internal/fixer"
	"github.com/usetrove/trove/internal/mcp/install"
	"github.com/usetrove/trove/internal/service"
	"github.com/usetrove/trove/internal/setup"
	"github.com/usetrove/trove/internal/store"
)

// Everything Trove itself creates directly inside its data directory. Anything
// else found there is not ours and is never deleted: the folder may be shared
// with another program, and a wrong delete cannot be undone.
var (
	ownedFiles = []string{
		"config.toml", "changes.json", "fixqueue.json", "gateway.json", "gateway.log",
		"mcp_installs.json", "mcp_trust.json", pluginStateFile, contribute.StateFile,
	}
	ownedDirs  = []string{"backups", "recovery", "hooks"}
	ownedGlobs = []string{"hooks.log*", ".mcp_trust-*", contribute.StateFile + ".*"}
)

type uninstallReport struct {
	Plan           []string           `json:"plan"`
	Applied        bool               `json:"applied"`
	Undone         []setup.UndoResult `json:"undone"`
	MCPRemoved     []string           `json:"mcp_removed"`
	Problems       []string           `json:"problems,omitempty"`
	Purged         []string           `json:"purged,omitempty"`
	Kept           []string           `json:"kept,omitempty"`
	DataDirRemoved bool               `json:"data_dir_removed"`
	Next           []string           `json:"next,omitempty"`
}

func newUninstall(a *App) *cobra.Command {
	var purge, yes, dry bool
	c := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove everything Trove added to your agents and, with --purge-data, its stored data",
		Long: "Shows what it will do, then (after you agree, or with --yes) stops the gateway and its background service,\n" +
			"restores every agent file Trove changed (byte for byte when you have not edited it since), and removes the\n" +
			"Trove MCP server from the agents it was registered with. Your usage history and settings in the data folder\n" +
			"are kept unless you add --purge-data, which deletes only files Trove created there and leaves anything else.\n" +
			"It never deletes data if an agent file could not be restored, because the backups are then still needed.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runUninstall(a, purge, yes, dry) },
	}
	c.Flags().BoolVar(&purge, "purge-data", false, "also delete the files Trove created in its data folder (history, backups, recovery store, settings)")
	c.Flags().BoolVar(&yes, "yes", false, "do it without asking")
	c.Flags().BoolVar(&dry, "dry-run", false, "show what would happen and stop")
	return c
}

func runUninstall(a *App, purge, yes, dry bool) error {
	dir, err := a.ResolveDataDir()
	if err != nil {
		return err
	}
	rep := uninstallReport{Undone: []setup.UndoResult{}, MCPRemoved: []string{}}

	changes, err := setup.LoadChanges(dir)
	if err != nil {
		return err
	}
	clis, err := install.LoadCLI(dir)
	if err != nil {
		return err
	}
	running := false
	if p, ok := readPID(dir); ok {
		_, running = probe(p.Addr)
	}
	unit := serviceUnit(a)
	owned, kept := purgeTargets(dir)
	ours := false
	if purge {
		ours = dataBaseIsTroves(dir)
	}

	if running {
		rep.Plan = append(rep.Plan, "Stop the running gateway.")
	}
	if unit != "" {
		rep.Plan = append(rep.Plan, "Stop and remove the background service ("+unit+").")
	}
	if len(changes) > 0 {
		rep.Plan = append(rep.Plan, fmt.Sprintf("Restore %d agent file change(s) from backups (gateway setting, status line, hooks, MCP entries, fixes).", len(changes)))
	}
	if len(clis) > 0 {
		rep.Plan = append(rep.Plan, fmt.Sprintf("Remove the Trove MCP server from %d agent registration(s) using the agent's own command.", len(clis)))
	}
	if purge {
		rep.Plan = append(rep.Plan, fmt.Sprintf("Permanently delete %d Trove file(s) or folder(s) in %s (backups last).", len(owned), dir))
		if len(kept) > 0 {
			rep.Plan = append(rep.Plan, "Leave alone, because Trove did not create them: "+strings.Join(kept, ", ")+".")
		}
	} else {
		rep.Plan = append(rep.Plan, "Keep the data folder "+dir+" (add --purge-data to delete what Trove stored there).")
	}
	if len(rep.Plan) == 1 && !purge {
		return a.emit(rep, "Nothing to uninstall: Trove has not changed any agent settings and nothing is running.\n"+
			"Your data folder "+dir+" is untouched; add --purge-data to delete what Trove stored there.")
	}

	text := "Uninstall will:\n  - " + strings.Join(rep.Plan, "\n  - ")
	if dry {
		return a.emit(rep, text+"\n\nDry run: nothing was changed.")
	}
	if !yes {
		if a.JSON {
			return a.emit(rep, text+"\n\nNothing was changed. Run again with --yes to apply.")
		}
		// Show the plan once, then ask once. With no answer possible (no terminal, or stdin at end of file),
		// say so and change nothing, rather than treating the silence as a refusal.
		_, _ = fmt.Fprintln(a.Out, text+"\n")
		ok, err := confirmUninstall(a)
		if err != nil {
			return errors.New("Nothing was changed. Uninstall needs your confirmation, and no answer could be read (there is no terminal to ask). " +
				"Run `trove uninstall --yes` to apply the plan above, or run it in a terminal.")
		}
		if !ok {
			return a.emit(rep, "Nothing was changed. Run again with --yes to apply.")
		}
	}

	// 1. Stop things that would keep rewriting settings or holding files.
	if _, err := stopGatewayProcess(dir); err != nil {
		rep.Problems = append(rep.Problems, err.Error())
	}
	rep.Problems = append(rep.Problems, stopService(a, true)...)

	// 2. Restore agent files.
	res, err := setup.UndoSelected(dir, nil)
	rep.Undone = append(rep.Undone, res...)
	if err != nil {
		rep.Problems = append(rep.Problems, "Restoring agent files stopped early: "+err.Error())
	}
	if merr := fixer.MarkReverted(dir, res, time.Now()); merr != nil {
		rep.Problems = append(rep.Problems, "Files were restored, but Trove could not update its record of the fixes: "+merr.Error())
	}
	leftAlone := 0
	for _, r := range res {
		if r.Action == "left_alone" {
			leftAlone++
		}
	}
	if leftAlone > 0 {
		rep.Problems = append(rep.Problems, fmt.Sprintf("%d agent file(s) were edited after Trove changed them, so Trove only removed its own lines or left them alone. Backups are kept in %s.", leftAlone, filepath.Join(dir, "backups")))
	}

	// 3. Unregister from agents' own CLIs.
	removed, failed, err := removeCLIRegistrations(a, dir)
	rep.MCPRemoved = append(rep.MCPRemoved, removed...)
	rep.Problems = append(rep.Problems, failed...)
	if err != nil {
		rep.Problems = append(rep.Problems, err.Error())
	}

	// 4. Data, only if everything above is cleanly reversed.
	if purge {
		left, _ := setup.LoadChanges(dir)
		recs, _ := install.LoadCLI(dir)
		switch {
		case len(rep.Problems) > 0 || len(left) > 0 || len(recs) > 0:
			rep.Problems = append(rep.Problems, "Your data was NOT deleted, because some agent changes could not be fully reversed and the backups are still needed. Fix the problems above, then run `trove uninstall --purge-data` again.")
		default:
			rep.Purged, rep.Kept, rep.DataDirRemoved = purgeData(dir, ours)
		}
	}
	rep.Applied = true

	if exe, err := troveBinary(a); err == nil && exe != "trove" {
		rep.Next = append(rep.Next, "Finally remove the program itself: "+exe+" (or `brew uninstall trove`, or `npm uninstall -g trove-cli`, if you installed it that way).")
	}
	if len(rep.Undone) > 0 {
		// Only when an agent's settings were actually changed back: nothing to restart for otherwise.
		rep.Next = append(rep.Next, "Restart Claude Code and Codex CLI so they stop using the old settings.")
	}

	var b strings.Builder
	for _, r := range rep.Undone {
		fmt.Fprintf(&b, "%s: %s (%s)\n", r.Agent, r.Detail, r.Path)
	}
	for _, r := range rep.MCPRemoved {
		fmt.Fprintf(&b, "Removed the Trove MCP server from %s.\n", r)
	}
	if len(rep.Purged) > 0 {
		fmt.Fprintf(&b, "Deleted %d Trove item(s) from %s.\n", len(rep.Purged), dir)
	}
	if len(rep.Kept) > 0 {
		fmt.Fprintf(&b, "Left in %s because Trove did not create them: %s\n", dir, strings.Join(rep.Kept, ", "))
	}
	for _, p := range rep.Problems {
		fmt.Fprintf(&b, "Problem: %s\n", p)
	}
	for _, n := range rep.Next {
		fmt.Fprintf(&b, "%s\n", n)
	}
	if err := a.emit(rep, strings.TrimRight(b.String(), "\n")); err != nil {
		return err
	}
	if len(rep.Problems) > 0 {
		return errors.New("uninstall finished with problems; see above")
	}
	return nil
}

// serviceUnit is the path of the installed background service definition, or "".
func serviceUnit(a *App) string {
	home, err := a.HomeDir()
	if err != nil {
		return ""
	}
	plan, err := service.Current(home, os.Getuid(), "/trove", nil)
	if err != nil {
		return ""
	}
	if _, err := os.Stat(plan.UnitPath); err != nil {
		return ""
	}
	return plan.UnitPath
}

// dataBaseIsTroves reports whether trove.db in dir has this program's schema
// history. The data folder name may be shared with another tool whose
// database must never be deleted by this command.
func dataBaseIsTroves(dir string) bool {
	db, err := store.OpenReadOnly(dir)
	if err != nil {
		return false
	}
	defer func() { _ = db.Close() }()
	v, err := db.CurrentVersion(context.Background())
	return err == nil && v >= 1
}

// purgeTargets lists the paths in dir that Trove creates, and the names of
// other entries that it does not.
func purgeTargets(dir string) (owned, kept []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	isOwned := func(name string) bool {
		for _, n := range ownedFiles {
			if n == name {
				return true
			}
		}
		for _, n := range ownedDirs {
			if n == name {
				return true
			}
		}
		for _, g := range ownedGlobs {
			if ok, _ := filepath.Match(g, name); ok {
				return true
			}
		}
		return strings.HasPrefix(name, store.DBFileName) // trove.db, -wal, -shm: checked separately
	}
	for _, e := range entries {
		if isOwned(e.Name()) {
			owned = append(owned, e.Name())
		} else {
			kept = append(kept, e.Name())
		}
	}
	sort.Strings(owned)
	sort.Strings(kept)
	return owned, kept
}

// purgeData deletes the files Trove created in dir. The database files go only
// when ours says they are Trove's. The folder itself is removed only if nothing
// else is left in it.
func purgeData(dir string, dbIsOurs bool) (purged, kept []string, dirRemoved bool) {
	owned, kept := purgeTargets(dir)
	for _, name := range owned {
		if strings.HasPrefix(name, store.DBFileName) && !dbIsOurs {
			kept = append(kept, name+" (not a Trove database of this version)")
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			kept = append(kept, name+" (could not delete: "+err.Error()+")")
			continue
		}
		purged = append(purged, name)
	}
	sort.Strings(kept)
	dirRemoved = os.Remove(dir) == nil // fails, harmlessly, if anything is left
	return purged, kept, dirRemoved
}
