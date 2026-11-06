// Package store owns Trove's SQLite database: opening it with safe
// permissions, versioned migrations, retention, and the non-blocking writer.
// It stores metadata only, never prompt text, code, file paths or tool output.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// DBFileName is the database file inside the data directory.
const DBFileName = "trove.db"

//go:embed migrations/*.sql
var migrationFS embed.FS

// DB wraps the SQLite handle.
type DB struct {
	*sql.DB
	path string
}

// Path returns the database file path.
func (d *DB) Path() string { return d.path }

// Open creates the data directory (0700) and database file (0600) if needed,
// opens the database in WAL mode and applies any pending migrations.
func Open(dataDir string) (*DB, error) {
	db, err := OpenNoMigrate(dataDir)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	tightenPermissions(dataDir)
	return db, nil
}

// tightenPermissions closes the gap MkdirAll and OpenFile leave: they only set a
// mode on something they create, so a data folder or database that already
// existed with looser permissions (an earlier version, a restored backup, a
// manual mkdir) stayed readable by other users. Only group and other access
// is removed; nothing is ever widened. Errors are ignored because `trove doctor`
// reports whatever is left (OpenNoMigrate deliberately does not call this, so
// doctor still sees the real state).
func tightenPermissions(dataDir string) {
	if runtime.GOOS == "windows" {
		return
	}
	narrow := func(path string, want fs.FileMode) {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&^want != 0 {
			_ = os.Chmod(path, fi.Mode().Perm()&want)
		}
	}
	narrow(dataDir, 0o700)
	for _, f := range []string{DBFileName, DBFileName + "-wal", DBFileName + "-shm", "config.toml"} {
		narrow(filepath.Join(dataDir, f), 0o600)
	}
}

// OpenNoMigrate is like Open but leaves the schema untouched (used by doctor).
func OpenNoMigrate(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("cannot create the data directory %s: %v. Check that the parent folder is writable, or pass --data-dir", dataDir, err)
	}
	path := filepath.Join(dataDir, DBFileName)
	// Create the file ourselves with 0600 so SQLite's WAL/SHM files inherit it.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // path is inside the user's own data directory
	if err != nil {
		return nil, fmt.Errorf("cannot open the database file %s: %v. Check its permissions", path, err)
	}
	_ = f.Close()

	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("cannot open the database %s: %v", path, err)
	}
	if err := sdb.Ping(); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("the database %s could not be opened: %v. If it is damaged, move it aside and Trove will create a new one", path, err)
	}
	return &DB{DB: sdb, path: path}, nil
}

// OpenReadOnly opens an existing database for reading only: it creates nothing,
// runs no migration and holds no write lock. It is what the status line uses, so
// it stays fast and can never disturb the gateway. A missing file is an error
// that satisfies os.IsNotExist.
func OpenReadOnly(dataDir string) (*DB, error) {
	path := filepath.Join(dataDir, DBFileName)
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(200)"
	if !strings.HasPrefix(filepath.ToSlash(path), "/") {
		dsn = "file:/" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(200)" // Windows drive paths
	}
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("cannot open the database %s: %v", path, err)
	}
	if err := sdb.Ping(); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("the database %s could not be opened for reading: %v", path, err)
	}
	return &DB{DB: sdb, path: path}, nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		name := e.Name()
		prefix, _, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(prefix)
		if !ok || err != nil {
			return nil, fmt.Errorf("migration %q must be named NNNN_description.sql", name)
		}
		b, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{v, name, string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i := 1; i < len(ms); i++ {
		if ms[i].version == ms[i-1].version {
			return nil, fmt.Errorf("duplicate migration version %d", ms[i].version)
		}
	}
	return ms, nil
}

// LatestVersion is the newest schema version this binary knows.
func LatestVersion() (int, error) {
	ms, err := loadMigrations()
	if err != nil || len(ms) == 0 {
		return 0, err
	}
	return ms[len(ms)-1].version, nil
}

// CurrentVersion is the schema version applied to this database (0 if none).
func (d *DB) CurrentVersion(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&n)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0, nil
		}
		return 0, err
	}
	return n, nil
}

// Migrate applies pending migrations, each in its own transaction. Running it
// again is a no-op. A database newer than this binary is refused.
func (d *DB) Migrate(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("cannot prepare migrations: %v", err)
	}
	cur, err := d.CurrentVersion(ctx)
	if err != nil {
		return err
	}
	if len(ms) > 0 && cur > ms[len(ms)-1].version {
		return fmt.Errorf("the database is at schema version %d but this Trove only knows up to %d. Upgrade Trove, or use a different --data-dir", cur, ms[len(ms)-1].version)
	}
	for _, m := range ms {
		if m.version <= cur {
			continue
		}
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s failed: %v. The database was left unchanged; please report this", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, m.version, time.Now().UnixMilli()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// PermissionProblems reports a data dir or database file that is more open
// than 0700 / 0600. It returns nothing on Windows, where mode bits are not meaningful.
func PermissionProblems(dataDir string) []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	var out []string
	check := func(path string, want fs.FileMode) {
		fi, err := os.Stat(path)
		if err != nil {
			return
		}
		if got := fi.Mode().Perm(); got&^want != 0 {
			out = append(out, fmt.Sprintf("%s has permissions %04o; run: chmod %04o %s", path, got, want, path))
		}
	}
	check(dataDir, 0o700)
	for _, f := range []string{DBFileName, DBFileName + "-wal", DBFileName + "-shm", "config.toml"} {
		check(filepath.Join(dataDir, f), 0o600)
	}
	return out
}

// RunRetention purges once immediately and then every interval until ctx ends.
// days is read on each run so config changes apply. Errors go to onErr (may be nil).
func (d *DB) RunRetention(ctx context.Context, days func() int, interval time.Duration, onErr func(error)) {
	run := func() {
		if _, err := d.Purge(ctx, days(), time.Now()); err != nil && onErr != nil && ctx.Err() == nil {
			onErr(err)
		}
	}
	run()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
