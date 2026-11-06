package cli

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/contribute"
	"github.com/usetrove/trove/internal/store"
)

// Local data controls. Export copies what Trove stores about you into a folder you
// choose; delete removes the usage data. Neither sends anything anywhere (rule 1):
// data you contributed lives on the receiver, and asking for its removal is a
// request you make yourself (see deleteContributedHint).

// dataDeleteTargets are the usage data. The rest of the data directory is the
// setup state `trove undo` and `trove uninstall` need to restore your agents'
// own files, so `trove data delete` leaves it.
var (
	dataDeleteFiles = []string{"contribute.json", "gateway.log"}
	dataDeleteDirs  = []string{"hooks", "recovery"}
	dataDeleteGlobs = []string{"hooks.log*", "contribute.json.*"}
)

func newData(a *App) *cobra.Command {
	c := &cobra.Command{
		Use:   "data",
		Short: "Export or delete the data Trove keeps on this computer",
		Long: "Trove keeps usage numbers (counts, sizes, hashes, model names, timings) in its data folder. Never prompts,\n" +
			"code, file paths or keys. `export` copies it into a folder you choose; `delete` removes it. Both work only on\n" +
			"this computer and send nothing. See docs/data.md for every field.",
	}
	c.AddCommand(newDataExport(a), newDataDelete(a))
	return c
}

// ---- export ----------------------------------------------------------------

type dataExportReport struct {
	Dir      string         `json:"dir"`
	Tables   map[string]int `json:"tables"`
	Files    []string       `json:"files"`
	Excluded []string       `json:"excluded"`
}

func newDataExport(a *App) *cobra.Command {
	var out string
	var withRecovery bool
	c := &cobra.Command{
		Use:   "export",
		Short: "Copy everything Trove stores about your usage into a folder",
		Long: "Writes every table of Trove's database as a CSV file, plus the other files Trove keeps (settings, hook\n" +
			"counters, your consent record), into a new folder. Backups of your agent config files are never copied, since they\n" +
			"can hold keys. The recovery store (raw command output you asked Trove to keep) is copied only with --include-recovery.",
		Example: "  trove data export\n  trove data export --out ~/trove-data-copy --json",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return runDataExport(a, out, withRecovery)
		},
	}
	c.Flags().StringVar(&out, "out", "", "folder to create (default: trove-export-<time> in the current folder)")
	c.Flags().BoolVar(&withRecovery, "include-recovery", false, "also copy the recovery store (raw command output; can contain anything your commands printed)")
	return c
}

func runDataExport(a *App, out string, withRecovery bool) error {
	dir, err := a.ResolveDataDir()
	if err != nil {
		return err
	}
	if out == "" {
		out = "trove-export-" + time.Now().Format("20060102-150405")
	}
	if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s already exists and is not empty. Choose a new folder with --out, so nothing is overwritten", out)
	}
	if err := os.MkdirAll(filepath.Join(out, "db"), 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %v. Choose a folder you can write to with --out", out, err)
	}
	rep := dataExportReport{Dir: out, Tables: map[string]int{}, Files: []string{}, Excluded: []string{}}

	db, err := openExisting(dir)
	if err != nil {
		return err
	}
	if db != nil {
		defer func() { _ = db.Close() }()
		tables, err := tableNames(db)
		if err != nil {
			return err
		}
		for _, t := range tables {
			n, err := exportTable(db, t, filepath.Join(out, "db", t+".csv"))
			if err != nil {
				return fmt.Errorf("cannot export table %s: %v", t, err)
			}
			rep.Tables[t] = n
		}
	}

	owned, _ := purgeTargets(dir)
	for _, name := range owned {
		switch {
		case strings.HasPrefix(name, store.DBFileName), name == pidFileName, name == "backups":
			continue // the database is exported as tables; the pid file is not data; backups can hold keys
		case name == "recovery" && !withRecovery:
			continue
		}
		copied, err := copyTree(filepath.Join(dir, name), filepath.Join(out, "files", name))
		if err != nil {
			return fmt.Errorf("cannot copy %s: %v", name, err)
		}
		rep.Files = append(rep.Files, copied...)
	}
	rep.Excluded = append(rep.Excluded, "backups/ (copies of your agent config files, which can hold keys)")
	if !withRecovery {
		rep.Excluded = append(rep.Excluded, "recovery/ (add --include-recovery to copy it)")
	}
	sort.Strings(rep.Files)
	manifest, _ := json.MarshalIndent(rep, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), append(manifest, '\n'), 0o600); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Exported to %s: %d table(s), %d file(s). Nothing was sent anywhere.\n", out, len(rep.Tables), len(rep.Files))
	for _, e := range rep.Excluded {
		fmt.Fprintf(&b, "  not copied: %s\n", e)
	}
	b.WriteString("Every field is described in docs/data.md.")
	return a.emit(rep, b.String())
}

func tableNames(db *store.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("cannot read Trove's database: %v. Run `trove doctor` for details", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func exportTable(db *store.DB, table, path string) (int, error) {
	// table comes from sqlite_master, never from user input.
	rows, err := db.QueryContext(context.Background(), `SELECT * FROM "`+strings.ReplaceAll(table, `"`, `""`)+`"`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // inside the export folder just created
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	w := csv.NewWriter(f)
	if err := w.Write(cols); err != nil {
		return 0, err
	}
	n := 0
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return 0, err
		}
		rec := make([]string, len(cols))
		for i, v := range vals {
			switch x := v.(type) {
			case nil:
				rec[i] = ""
			case []byte:
				rec[i] = string(x)
			default:
				rec[i] = fmt.Sprint(x)
			}
		}
		if err := w.Write(rec); err != nil {
			return 0, err
		}
		n++
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return 0, err
	}
	return n, rows.Err()
}

// copyTree copies a file or folder with owner-only permissions and returns the
// destination paths relative to the export folder's "files" directory.
func copyTree(src, dst string) ([]string, error) {
	var copied []string
	root := filepath.Dir(dst)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil // never follow links out of the data folder
		}
		in, err := os.Open(p) //nolint:gosec // inside the user's data folder
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // inside the export folder
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		r, _ := filepath.Rel(filepath.Dir(root), target)
		copied = append(copied, filepath.ToSlash(r))
		return nil
	})
	return copied, err
}

// ---- delete ----------------------------------------------------------------

type dataDeleteReport struct {
	Plan            []string `json:"plan"`
	Applied         bool     `json:"applied"`
	Deleted         []string `json:"deleted,omitempty"`
	Kept            []string `json:"kept,omitempty"`
	Problems        []string `json:"problems,omitempty"`
	ContributedData string   `json:"contributed_data,omitempty"`
}

func newDataDelete(a *App) *cobra.Command {
	var yes, dry bool
	c := &cobra.Command{
		Use:   "delete",
		Short: "Delete the usage data Trove keeps on this computer",
		Long: "Deletes Trove's database (requests, sessions, receipts, features, tuning), the hook counters and logs, the\n" +
			"recovery store and your consent record. It keeps the setup state that `trove undo` and `trove uninstall` need to restore\n" +
			"your agents' own files (settings, change records, backups), so you can still remove Trove cleanly afterwards.\n" +
			"It sends nothing. If you ever shared an anonymous summary, it prints how to ask the receiver to delete it.",
		Example: "  trove data delete --dry-run\n  trove data delete --yes",
		Args:    cobra.NoArgs,
		RunE:    func(*cobra.Command, []string) error { return runDataDelete(a, yes, dry) },
	}
	c.Flags().BoolVar(&yes, "yes", false, "do it without asking")
	c.Flags().BoolVar(&dry, "dry-run", false, "show what would be deleted and delete nothing")
	return c
}

// deleteContributedHint is what a user does to remove a summary they shared. It
// is printed, never executed: Trove makes no call that is not on its short list
// (CLAUDE.md rule 1).
func deleteContributedHint(endpoint, installID string) string {
	if installID == "" || endpoint == "" {
		return ""
	}
	return fmt.Sprintf("You turned on sharing earlier. To ask the receiver to delete what it holds for this install, run:\n"+
		"  curl -X DELETE %s/%s\nSave that install id first; this command deletes the local copy of it.", strings.TrimRight(endpoint, "/"), installID)
}

func runDataDelete(a *App, yes, dry bool) error {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	rep := dataDeleteReport{Plan: []string{}}
	if p, ok := readPID(dir); ok {
		if _, up := probe(p.Addr); up {
			return fmt.Errorf("the Trove gateway is running (pid %d) and has the database open. Run `trove gateway stop`, then try again", p.PID)
		}
	}
	targets, kept := dataTargets(dir)
	dbOurs := true
	for _, t := range targets {
		if strings.HasPrefix(t, store.DBFileName) {
			dbOurs = dataBaseIsTroves(dir)
			break
		}
	}
	for _, t := range targets {
		if strings.HasPrefix(t, store.DBFileName) && !dbOurs {
			rep.Kept = append(rep.Kept, t+" (not a Trove database of this version)")
			continue
		}
		rep.Plan = append(rep.Plan, t)
	}
	rep.Kept = append(rep.Kept, kept...)
	if st, err := contribute.LoadState(dir); err == nil {
		rep.ContributedData = deleteContributedHint(cfg.Contribute.Endpoint, st.InstallID)
	}
	if len(rep.Plan) == 0 {
		return a.emit(rep, "Nothing to delete: Trove has no usage data in "+dir+".")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "This would delete %d item(s) in %s:\n", len(rep.Plan), dir)
	for _, p := range rep.Plan {
		fmt.Fprintf(&b, "  %s\n", p)
	}
	if rep.ContributedData != "" {
		b.WriteString(rep.ContributedData + "\n")
	}
	if dry {
		b.WriteString("Dry run: nothing was deleted.")
		return a.emit(rep, b.String())
	}
	if !yes {
		if !a.JSON {
			_, _ = fmt.Fprint(a.Out, b.String())
		}
		if !a.confirm("Delete this data? This cannot be undone.") {
			return fmt.Errorf("nothing was deleted. Run `trove data delete --yes` to confirm, or `--dry-run` to preview")
		}
	}
	b.Reset()
	for _, name := range rep.Plan {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			rep.Problems = append(rep.Problems, name+": "+err.Error())
			continue
		}
		rep.Deleted = append(rep.Deleted, name)
	}
	rep.Applied = true
	fmt.Fprintf(&b, "Deleted %d item(s) from %s.", len(rep.Deleted), dir)
	if len(rep.Problems) > 0 {
		fmt.Fprintf(&b, "\n%d item(s) could not be deleted:\n  %s\nClose anything using them and run this again.", len(rep.Problems), strings.Join(rep.Problems, "\n  "))
	}
	if rep.ContributedData != "" {
		b.WriteString("\n" + rep.ContributedData)
	}
	if err := a.emit(rep, b.String()); err != nil {
		return err
	}
	if len(rep.Problems) > 0 {
		return fmt.Errorf("some data could not be deleted")
	}
	return nil
}

// dataTargets lists the usage-data entries present in dir, and the entries that
// stay (setup state, or files that are not Trove's).
func dataTargets(dir string) (targets, kept []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	match := func(name string) bool {
		for _, n := range dataDeleteFiles {
			if n == name {
				return true
			}
		}
		for _, n := range dataDeleteDirs {
			if n == name {
				return true
			}
		}
		for _, g := range dataDeleteGlobs {
			if ok, _ := filepath.Match(g, name); ok {
				return true
			}
		}
		return strings.HasPrefix(name, store.DBFileName)
	}
	for _, e := range entries {
		if match(e.Name()) {
			targets = append(targets, e.Name())
		} else {
			kept = append(kept, e.Name())
		}
	}
	sort.Strings(targets)
	sort.Strings(kept)
	return targets, kept
}
