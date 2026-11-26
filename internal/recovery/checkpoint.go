package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Session checkpoints (hook_session_checkpoint, technique B31) are the second kind of text the recovery store
// keeps: a short, structured note of where a session was (the latest request texts, files changed, branch, test
// commands that were failing), so it can be added back after /compact or /clear. They live in their own folder,
// recovery/checkpoints, under the same rules as stored outputs (PRINCIPLES.md rule 7): files are mode 0600 in a
// 0700 folder, every note is capped, the folder is capped, and notes expire. There is one note per working
// directory (its slot); a newer note replaces the older one. Nothing here touches the network or the database.

// Limits of the checkpoint store. They are fixed rather than taken from [recovery], because a note is small and
// short-lived by design: it only has to bridge one compaction or one /clear.
const (
	// CheckpointMaxFileBytes caps one note on disk. A note that does not fit after its fields are capped is
	// refused, never cut, so a stored note is always well formed.
	CheckpointMaxFileBytes = 8 << 10
	// CheckpointMaxTotalBytes caps the folder. The oldest notes go first.
	CheckpointMaxTotalBytes = 1 << 20
	// CheckpointTTL is how long a note is kept.
	CheckpointTTL = 72 * time.Hour

	checkpointVersion = 1
	checkpointExt     = ".ckpt"
	slotLen           = 16
)

// Field caps, applied by Cap before a note is stored. Each text is cut on a character boundary.
const (
	MaxTaskItems     = 3
	MaxTaskBytes     = 300
	MaxDecisionItems = 3
	MaxDecisionBytes = 200
	MaxFileItems     = 10
	MaxFileBytes     = 200
	MaxBranchBytes   = 100
	MaxFailingItems  = 3
	MaxFailingBytes  = 200
	MaxReplyBytes    = 400
)

// Checkpoint is one session note. Every field is optional; an empty note is never stored.
type Checkpoint struct {
	V       int    `json:"v"`
	SavedAt int64  `json:"saved_at"` // unix ms
	Session string `json:"session"`  // the hashed session key, never the agent's own id
	// Reason is what triggered the note: precompact, session_end or fill_<percent>.
	Reason string `json:"reason"`
	// Task is the latest request texts the user typed, oldest first.
	Task []string `json:"task,omitempty"`
	// Decisions are sentences from the agent's recent replies that state a choice ("going with", "instead of").
	Decisions []string `json:"decisions,omitempty"`
	// Files are the files the session edited, relative to the working directory when inside it, oldest first.
	Files []string `json:"files,omitempty"`
	// Branch is the git branch, or "detached at <sha>".
	Branch string `json:"branch,omitempty"`
	// FailingTests are test commands whose latest run in the session failed.
	FailingTests []string `json:"failing_tests,omitempty"`
	// LastReply is the end of the agent's last reply.
	LastReply string `json:"last_reply,omitempty"`
}

// Empty reports whether the note says nothing worth restoring.
func (c Checkpoint) Empty() bool {
	return len(c.Task) == 0 && len(c.Decisions) == 0 && len(c.Files) == 0 && c.Branch == "" &&
		len(c.FailingTests) == 0 && c.LastReply == ""
}

// Cap applies the field caps: the newest items are kept, each text is cut to its limit.
func (c Checkpoint) Cap() Checkpoint {
	c.Task = capList(c.Task, MaxTaskItems, MaxTaskBytes)
	c.Decisions = capList(c.Decisions, MaxDecisionItems, MaxDecisionBytes)
	c.Files = capList(c.Files, MaxFileItems, MaxFileBytes)
	c.Branch = cutText(c.Branch, MaxBranchBytes)
	c.FailingTests = capList(c.FailingTests, MaxFailingItems, MaxFailingBytes)
	c.LastReply = cutText(c.LastReply, MaxReplyBytes)
	return c
}

func capList(xs []string, n, each int) []string {
	if len(xs) > n {
		xs = xs[len(xs)-n:]
	}
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x = cutText(x, each); x != "" {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// cutText collapses whitespace to single spaces and cuts s to at most n bytes on a character boundary, marking a
// cut with "…".
func cutText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	s = s[:n-len("…")]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// ErrNoCheckpoint is returned when no usable note exists for a slot: none was stored, it expired, it was evicted to
// stay under the size cap, or it was damaged (the file is removed).
var ErrNoCheckpoint = errors.New("no session checkpoint is available for this folder")

// Slot is the name a working directory's note is stored under: a hash, so the folder name is never on disk.
func Slot(cwd string) string {
	sum := sha256.Sum256([]byte("trove-checkpoint\x00" + filepath.Clean(cwd)))
	return hex.EncodeToString(sum[:])[:slotLen]
}

// CheckpointStore is the checkpoints folder. It is safe for concurrent use in one process; across processes every
// write is a rename, so a reader sees a whole note or none.
type CheckpointStore struct {
	dir string
	now func() time.Time
	mu  sync.Mutex
}

// OpenCheckpoints creates the folder if needed and removes expired notes. now may be nil.
func OpenCheckpoints(dir string, now func() time.Time) (*CheckpointStore, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cannot create the checkpoint folder %s: %w. Check that you can write to your Trove data folder", dir, err)
	}
	_ = os.Chmod(dir, 0o700) //nolint:gosec // a directory needs the execute bit; 0700 is owner-only
	s := &CheckpointStore{dir: dir, now: now}
	s.Sweep()
	return s, nil
}

// CheckpointDir is where the notes of a recovery store at recoveryDir live.
func CheckpointDir(recoveryDir string) string { return filepath.Join(recoveryDir, "checkpoints") }

func validSlot(slot string) bool { return len(slot) == slotLen && idRe.MatchString(slot) }

func (s *CheckpointStore) path(slot string) string { return filepath.Join(s.dir, slot+checkpointExt) }

// Save caps the note's fields, stamps it and stores it under slot, replacing the slot's older note. It returns the
// number of bytes written. An empty note, or one still over CheckpointMaxFileBytes, is refused.
func (s *CheckpointStore) Save(slot string, c Checkpoint) (int, error) {
	if !validSlot(slot) {
		return 0, fmt.Errorf("%q is not a checkpoint slot", slot)
	}
	c = c.Cap()
	if c.Empty() {
		return 0, fmt.Errorf("the checkpoint is empty, so nothing was stored")
	}
	c.V = checkpointVersion
	if c.SavedAt == 0 {
		c.SavedAt = s.now().UnixMilli()
	}
	b, err := json.Marshal(c)
	if err != nil {
		return 0, err
	}
	if len(b) > CheckpointMaxFileBytes {
		return 0, fmt.Errorf("the checkpoint is %d bytes, over the %d-byte limit, so it was not stored", len(b), CheckpointMaxFileBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return 0, fmt.Errorf("cannot write to the checkpoint folder: %w", err)
	}
	name := tmp.Name()
	_ = os.Chmod(name, 0o600)
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return 0, fmt.Errorf("cannot write to the checkpoint folder: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return 0, err
	}
	p := s.path(slot)
	if err := os.Rename(name, p); err != nil {
		_ = os.Remove(name)
		return 0, fmt.Errorf("cannot write to the checkpoint folder: %w", err)
	}
	now := s.now()
	_ = os.Chtimes(p, now, now)
	s.evictLocked(slot)
	return len(b), nil
}

// Latest returns the slot's note if it is younger than maxAge (and CheckpointTTL). A note that is expired, too
// large, unreadable or not a note is removed and ErrNoCheckpoint returned.
func (s *CheckpointStore) Latest(slot string, maxAge time.Duration) (Checkpoint, error) {
	if !validSlot(slot) {
		return Checkpoint{}, ErrNoCheckpoint
	}
	if maxAge <= 0 || maxAge > CheckpointTTL {
		maxAge = CheckpointTTL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.path(slot)
	fi, err := os.Stat(p)
	if err != nil {
		return Checkpoint{}, ErrNoCheckpoint
	}
	now := s.now()
	if now.Sub(fi.ModTime()) > CheckpointTTL || fi.Size() > CheckpointMaxFileBytes {
		_ = os.Remove(p)
		return Checkpoint{}, ErrNoCheckpoint
	}
	b, err := os.ReadFile(p) //nolint:gosec // p is built from a validSlot-checked slot under the checkpoint folder
	if err != nil {
		return Checkpoint{}, ErrNoCheckpoint
	}
	var c Checkpoint
	if json.Unmarshal(b, &c) != nil || c.V != checkpointVersion || c.SavedAt <= 0 {
		_ = os.Remove(p) // damaged, or from another version: never restored
		return Checkpoint{}, ErrNoCheckpoint
	}
	age := now.Sub(time.UnixMilli(c.SavedAt))
	if age > CheckpointTTL {
		_ = os.Remove(p)
		return Checkpoint{}, ErrNoCheckpoint
	}
	if age > maxAge || age < -time.Minute {
		return Checkpoint{}, ErrNoCheckpoint // kept, but too old (or from the future) to count as this session's
	}
	c = c.Cap() // a hand-edited note is held to the same caps as a stored one
	if c.Empty() {
		return Checkpoint{}, ErrNoCheckpoint
	}
	return c, nil
}

// Sweep removes expired notes and stray temp files. Errors are ignored: a note that cannot be removed now is
// removed on a later sweep, and Latest never returns an expired one.
func (s *CheckpointStore) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	des, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	now := s.now()
	for _, d := range des {
		fi, err := d.Info()
		if err != nil {
			continue
		}
		name := d.Name()
		switch {
		case strings.HasPrefix(name, ".tmp-") && now.Sub(fi.ModTime()) > time.Minute:
			_ = os.Remove(filepath.Join(s.dir, name))
		case strings.HasSuffix(name, checkpointExt) && now.Sub(fi.ModTime()) > CheckpointTTL:
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
}

// evictLocked removes the oldest notes, never keep, until the folder is under CheckpointMaxTotalBytes.
func (s *CheckpointStore) evictLocked(keep string) {
	des, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	type ent struct {
		slot string
		size int64
		mod  time.Time
	}
	var ents []ent
	var total int64
	for _, d := range des {
		name := d.Name()
		if !strings.HasSuffix(name, checkpointExt) || !validSlot(strings.TrimSuffix(name, checkpointExt)) {
			continue
		}
		fi, err := d.Info()
		if err != nil {
			continue
		}
		ents = append(ents, ent{strings.TrimSuffix(name, checkpointExt), fi.Size(), fi.ModTime()})
		total += fi.Size()
	}
	if total <= CheckpointMaxTotalBytes {
		return
	}
	sort.Slice(ents, func(i, j int) bool {
		if !ents[i].mod.Equal(ents[j].mod) {
			return ents[i].mod.Before(ents[j].mod)
		}
		return ents[i].slot < ents[j].slot
	})
	for _, e := range ents {
		if total <= CheckpointMaxTotalBytes {
			break
		}
		if e.slot == keep {
			continue
		}
		if os.Remove(s.path(e.slot)) == nil {
			total -= e.size
		}
	}
}
