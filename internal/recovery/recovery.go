// Package recovery is the local store that keeps the full text of tool output
// Trove shortened, so nothing it trims is ever lost. It is the one place tool
// output is written to disk (rule 7): files are mode 0600 in a 0700 directory,
// the total size is capped, and entries expire. Nothing here touches the
// network.
//
// An output's ID is the first 16 hex characters of the sha256 of its bytes, the
// same prefix the output trimmer writes in its marker line. The same bytes
// therefore always get the same ID.
package recovery

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Defaults for the store limits.
const (
	DefaultMaxBytes = 200 << 20
	DefaultTTL      = 7 * 24 * time.Hour
	idLen           = 16
	fileExt         = ".out"
)

var idRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ErrNotFound is returned for an ID that was never stored, has expired, was
// evicted to stay under the size cap, or whose stored bytes no longer hash to
// the ID (the file is removed). Callers report the output as unavailable; they
// never substitute empty text.
var ErrNotFound = errors.New("the stored output is not available: no output is stored under that ID, or it expired, was removed to stay under the size limit, or failed its integrity check and was discarded")

// Options configures a Store. Zero values pick the defaults.
type Options struct {
	MaxBytes int64
	TTL      time.Duration
	Now      func() time.Time
}

// Store is a directory of stored outputs. It is safe for concurrent use.
type Store struct {
	dir string
	max int64
	ttl time.Duration
	now func() time.Time
	mu  sync.Mutex
}

// Open creates the directory if needed and returns a store. Expired entries are
// removed on open.
func Open(dir string, o Options) (*Store, error) {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.TTL <= 0 {
		o.TTL = DefaultTTL
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cannot create the recovery folder %s: %w. Check that you can write to your Trove data folder", dir, err)
	}
	_ = os.Chmod(dir, 0o700) //nolint:gosec // a directory needs the execute bit; 0700 is owner-only
	s := &Store{dir: dir, max: o.MaxBytes, ttl: o.TTL, now: o.Now}
	if _, err := s.Sweep(); err != nil {
		return nil, err
	}
	return s, nil
}

// ID returns the ID Put would give these bytes.
func ID(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:idLen]
}

// matchesID reports whether data hashes to id. It hashes every byte, not a prefix, and compares the two IDs in
// constant time, so the check does not leak how much of an ID matched.
func matchesID(data []byte, id string) bool {
	return subtle.ConstantTimeCompare([]byte(ID(data)), []byte(id)) == 1
}

// ValidID reports whether id has the shape of an ID. Every lookup checks this
// first, so an ID can never be used as a path.
func ValidID(id string) bool { return idRe.MatchString(id) }

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+fileExt) }

// Put stores data and returns its ID. Storing the same bytes again refreshes
// the entry's age. An output larger than the whole size cap is refused rather
// than stored partially, because partial storage would break "always
// recoverable".
func (s *Store) Put(data []byte) (string, error) {
	if int64(len(data)) > s.max {
		return "", fmt.Errorf("output is %d bytes, larger than the recovery size limit of %d bytes. Raise [recovery] max_mb in config.toml", len(data), s.max)
	}
	id := ID(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.path(id)
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("cannot write to the recovery folder: %w", err)
	}
	name := tmp.Name()
	_ = os.Chmod(name, 0o600)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("cannot write to the recovery folder: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := os.Rename(name, p); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("cannot write to the recovery folder: %w", err)
	}
	now := s.now()
	_ = os.Chtimes(p, now, now)
	if err := s.evictLocked(id); err != nil {
		return "", err
	}
	return id, nil
}

// Get returns the stored output. When start is above 0 or end is above 0 it
// returns only that 1-based inclusive line range (end 0 means to the last
// line). The second result is the total line count of the stored output.
func (s *Store) Get(id string, start, end int) (string, int, error) {
	if !ValidID(id) {
		return "", 0, fmt.Errorf("%q is not an output ID. IDs are 16 hex characters, as shown in the trimmed output or trove_run result", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.path(id)
	fi, err := os.Stat(p)
	if err != nil {
		return "", 0, ErrNotFound
	}
	if s.now().Sub(fi.ModTime()) > s.ttl {
		_ = os.Remove(p)
		return "", 0, ErrNotFound
	}
	b, err := os.ReadFile(p) //nolint:gosec // p is built from a ValidID-checked id under the recovery directory
	if err != nil {
		return "", 0, ErrNotFound
	}
	if !matchesID(b, id) {
		// The file no longer holds the bytes that were stored under this ID: bit rot, a damaged write, or an
		// edit. It is not returned as the tool's output. It is removed, so later calls do not keep reading it,
		// and the caller reports the output as unavailable.
		_ = os.Remove(p)
		return "", 0, ErrNotFound
	}
	text := string(b)
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)
	if start <= 0 && end <= 0 {
		return text, total, nil
	}
	if start < 1 {
		start = 1
	}
	if end <= 0 || end > total {
		end = total
	}
	if start > total || start > end {
		return "", total, fmt.Errorf("line range %d-%d is outside this output, which has %d lines", start, end, total)
	}
	return strings.Join(lines[start-1:end], ""), total, nil
}

// Sweep removes expired entries and returns how many it removed.
func (s *Store) Sweep() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ents, err := s.listLocked()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		if s.now().Sub(e.mod) > s.ttl {
			if os.Remove(s.path(e.id)) == nil {
				n++
			}
		}
	}
	return n, nil
}

// Stats is the current size of the store.
type Stats struct {
	Entries int
	Bytes   int64
}

// Stats reports how much the store holds.
func (s *Store) Stats() (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ents, err := s.listLocked()
	if err != nil {
		return Stats{}, err
	}
	var st Stats
	for _, e := range ents {
		st.Entries++
		st.Bytes += e.size
	}
	return st, nil
}

type entry struct {
	id   string
	size int64
	mod  time.Time
}

func (s *Store) listLocked() ([]entry, error) {
	des, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("cannot read the recovery folder: %w", err)
	}
	var out []entry
	for _, d := range des {
		name := d.Name()
		if !strings.HasSuffix(name, fileExt) || !ValidID(strings.TrimSuffix(name, fileExt)) {
			continue
		}
		fi, err := d.Info()
		if err != nil {
			continue
		}
		out = append(out, entry{id: strings.TrimSuffix(name, fileExt), size: fi.Size(), mod: fi.ModTime()})
	}
	return out, nil
}

// evictLocked removes the oldest entries, never keep, until the total is under
// the cap.
func (s *Store) evictLocked(keep string) error {
	ents, err := s.listLocked()
	if err != nil {
		return err
	}
	var total int64
	for _, e := range ents {
		total += e.size
	}
	if total <= s.max {
		return nil
	}
	sort.Slice(ents, func(i, j int) bool {
		if !ents[i].mod.Equal(ents[j].mod) {
			return ents[i].mod.Before(ents[j].mod)
		}
		return ents[i].id < ents[j].id
	})
	for _, e := range ents {
		if total <= s.max {
			break
		}
		if e.id == keep {
			continue
		}
		if os.Remove(s.path(e.id)) == nil {
			total -= e.size
		}
	}
	return nil
}
