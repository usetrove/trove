package stress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usetrove/trove/internal/recovery"
	"github.com/usetrove/trove/internal/store"
)

// Storage fault cases. None of these is skipped. Two are simulated, and each says so in its violation or note:
//
//   - kill mid-write: the store is not killed mid-call. The partial temporary file that an interrupted write
//     leaves behind is created by hand, because the store writes to a temporary name and renames it into
//     place. Reopening must neither serve that partial file nor lose complete entries.
//   - full disk: no disk is filled. The error a full disk returns is injected at the storage seam, and a real
//     database write failure is produced by writing through a closed handle.
//   - corrupted store and read-only folder: real. A stored blob is tampered with on disk, a database file is
//     overwritten in place, and a read-only handle is used through SQLite's own read-only mode, which does not
//     depend on file permissions and so works the same when run as root.

// killMidWriteFault: a Put that is killed before the rename leaves a partial temporary file and no final entry.
func (e *env) killMidWriteFault() []Violation {
	dir := filepath.Join(e.dir, "kill")
	rs, err := recovery.Open(dir, recovery.Options{})
	if err != nil {
		return []Violation{{"setup", err.Error()}}
	}
	complete := []byte("complete output stored before the kill")
	idA, err := rs.Put(complete)
	if err != nil {
		return []Violation{{"setup", err.Error()}}
	}
	interrupted := []byte(strings.Repeat("this output was being written when the process died; ", 40))
	idB := recovery.ID(interrupted)
	// The partial file a killed write leaves: half the bytes, under the store's temporary-name prefix.
	if err := os.WriteFile(filepath.Join(dir, ".tmp-killed"), interrupted[:len(interrupted)/2], 0o600); err != nil {
		return []Violation{{"setup", err.Error()}}
	}
	// Reopen, as the next start would.
	rs, err = recovery.Open(dir, recovery.Options{})
	if err != nil {
		return []Violation{{InvFailOpenOnError, "reopening after an interrupted write failed: " + err.Error()}}
	}
	var vs []Violation
	if got, _, err := rs.Get(idA, 0, 0); err != nil || got != string(complete) {
		vs = append(vs, Violation{InvRecovery, "a complete entry was lost or changed after an interrupted write"})
	}
	if got, _, err := rs.Get(idB, 0, 0); err == nil {
		vs = append(vs, Violation{InvRecovery, fmt.Sprintf("an interrupted write was served as a complete output (%d bytes)", len(got))})
	}
	if _, err := rs.Put([]byte("written after the restart")); err != nil {
		vs = append(vs, Violation{InvFailOpenOnError, "the store refused new writes after an interrupted write"})
	}
	return vs
}

// corruptStoreFault: a stored output tampered with on disk must never be served as genuine, and a database
// overwritten in place must fail with an error rather than a panic.
func (e *env) corruptStoreFault() []Violation {
	var vs []Violation
	dir := filepath.Join(e.dir, "corrupt-blob")
	rs, err := recovery.Open(dir, recovery.Options{})
	if err != nil {
		return []Violation{{"setup", err.Error()}}
	}
	orig := []byte("the output the tool printed, stored intact")
	id, err := rs.Put(orig)
	if err != nil {
		return []Violation{{"setup", err.Error()}}
	}
	path, err := findBlob(dir, id)
	if err != nil {
		return []Violation{{"setup", err.Error()}}
	}
	data, err := os.ReadFile(path) //nolint:gosec // a file in the suite's own scratch folder
	if err != nil {
		return []Violation{{"setup", err.Error()}}
	}
	data[0] ^= 0xFF                                         // same length, different content: a bit-rot or hostile edit
	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // a file in the suite's own scratch folder
		return []Violation{{"setup", err.Error()}}
	}
	if got, _, err := rs.Get(id, 0, 0); err == nil && got != string(orig) {
		vs = append(vs, Violation{InvRecovery, "a tampered stored output was served as if it were the tool's output"})
	}

	vs = append(vs, corruptDatabaseCheck(filepath.Join(e.dir, "corrupt-db"))...)
	return vs
}

// findBlob returns the stored file for an ID. Temporary files are not blobs.
func findBlob(dir, id string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), id) && !strings.HasPrefix(en.Name(), ".tmp-") {
			return filepath.Join(dir, en.Name()), nil
		}
	}
	return "", fmt.Errorf("no stored file for the test output")
}

// corruptDatabaseCheck writes real rows, overwrites part of the database file in place, and reopens it. The
// outcome must be an error with a message, or a clean open; a panic is the failure.
func corruptDatabaseCheck(dir string) (out []Violation) {
	defer func() {
		if r := recover(); r != nil {
			out = []Violation{{InvFailOpenOnError, fmt.Sprintf("opening a corrupted database panicked: %v", r)}}
		}
	}()
	db, err := store.Open(dir)
	if err != nil {
		return []Violation{{"setup", err.Error()}}
	}
	w := store.NewWriter(db, store.WriterOptions{})
	for i := 0; i < 200; i++ {
		w.Enqueue(store.Request{TS: time.Now(), SessionKey: "corrupt", Agent: "claude_code", Provider: "anthropic",
			Model: "claude-sonnet-5-5", Endpoint: "/v1/messages", StatusCode: 200})
	}
	_ = w.Close(context.Background())
	_ = db.Close()

	path := filepath.Join(dir, store.DBFileName)
	f, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // the database in the suite's own scratch folder
	if err != nil {
		return []Violation{{"setup", err.Error()}}
	}
	info, _ := f.Stat()
	off := info.Size() / 2
	if _, err := f.WriteAt(bytes.Repeat([]byte{0xFF}, 512), off); err != nil {
		_ = f.Close()
		return []Violation{{"setup", err.Error()}}
	}
	_ = f.Close()

	db2, err := store.Open(dir)
	if err != nil {
		if strings.TrimSpace(err.Error()) == "" {
			return []Violation{{InvFailOpenOnError, "a corrupted database gave an empty error"}}
		}
		return nil
	}
	defer func() { _ = db2.Close() }()
	if _, err := db2.CurrentVersion(context.Background()); err != nil {
		if strings.TrimSpace(err.Error()) == "" {
			return []Violation{{InvFailOpenOnError, "a corrupted database gave an empty error"}}
		}
	}
	return nil
}

// fullDiskFault: the engine's recovery store and the database writer both see a write fail the way a full disk
// makes it fail. The request still goes through unchanged, and the writer accounts for every row.
func (e *env) fullDiskFault() []Violation {
	var vs []Violation
	// The recovery store's refusal is injected at the seam; the engine must forward the original.
	vs = append(vs, e.engineFault(fullDiskRecovery{})...)

	dir := filepath.Join(e.dir, "full")
	db, err := store.Open(dir)
	if err != nil {
		return append(vs, Violation{"setup", err.Error()})
	}
	_ = db.Close() // every write through this handle now fails, as a database on a full disk does
	w := store.NewWriter(db, store.WriterOptions{})
	const n = 20
	for i := 0; i < n; i++ {
		w.Enqueue(store.Request{TS: time.Now(), SessionKey: "full", Agent: "claude_code", Provider: "anthropic",
			Model: "claude-sonnet-5-5", Endpoint: "/v1/messages", StatusCode: 200})
	}
	_ = w.Close(context.Background())
	st := w.Stats()
	if st.Written != 0 {
		vs = append(vs, Violation{InvFailOpenOnError, "rows reported as written to a failed database"})
	}
	if st.Written+st.Dropped+st.Failed != n {
		vs = append(vs, Violation{InvFailOpenOnError, fmt.Sprintf("%d rows in, %d accounted for on a failed database", n, st.Written+st.Dropped+st.Failed)})
	}
	return vs
}

// fullDiskRecovery refuses every store with the error a full disk returns.
type fullDiskRecovery struct{}

func (fullDiskRecovery) Put([]byte) (string, error) {
	return "", errors.New("write recovery output: no space left on device (injected)")
}

// readOnlyFault: a read-only handle must read, and every write through it must fail and be counted. SQLite's
// read-only mode enforces this, not file permissions.
func (e *env) readOnlyFault() []Violation {
	dir := filepath.Join(e.dir, "readonly")
	db, err := store.Open(dir)
	if err != nil {
		return []Violation{{"setup", err.Error()}}
	}
	_ = db.Close()

	ro, err := store.OpenReadOnly(dir)
	if err != nil {
		return []Violation{{InvFailOpenOnError, "a read-only open of an existing database failed: " + err.Error()}}
	}
	defer func() { _ = ro.Close() }()
	var vs []Violation
	if _, err := ro.CurrentVersion(context.Background()); err != nil {
		vs = append(vs, Violation{InvFailOpenOnError, "a read-only handle could not read the database"})
	}
	w := store.NewWriter(ro, store.WriterOptions{})
	const n = 20
	for i := 0; i < n; i++ {
		w.Enqueue(store.Request{TS: time.Now(), SessionKey: "ro", Agent: "claude_code", Provider: "anthropic",
			Model: "claude-sonnet-5-5", Endpoint: "/v1/messages", StatusCode: 200})
	}
	_ = w.Close(context.Background())
	st := w.Stats()
	if st.Written != 0 {
		vs = append(vs, Violation{InvFailOpenOnError, "rows were written through a read-only handle"})
	}
	if st.Written+st.Dropped+st.Failed != n {
		vs = append(vs, Violation{InvFailOpenOnError, fmt.Sprintf("%d rows in, %d accounted for on a read-only database", n, st.Written+st.Dropped+st.Failed)})
	}
	return vs
}
