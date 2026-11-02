package contribute

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StateFile is the consent record inside the data directory.
const StateFile = "contribute.json"

// Interval is the shortest time between two automatic uploads.
const Interval = 7 * 24 * time.Hour

// State is the user's consent and the upload bookkeeping. It is the only thing
// that can turn upload on, and it is off until `trove contribute enable` writes it.
type State struct {
	Enabled bool `json:"enabled"`
	// InstallID is a random identifier with no link to the person or machine. It
	// lets the receiver drop repeats and delete a user's rows on request.
	InstallID string `json:"install_id,omitempty"`
	// LastSent is the day (YYYY-MM-DD, UTC) of the last successful upload.
	LastSent string `json:"last_sent,omitempty"`
}

// LoadState reads the consent record. A missing file is the off state.
func LoadState(dir string) (State, error) {
	b, err := os.ReadFile(filepath.Join(dir, StateFile)) //nolint:gosec // inside the user's own data directory
	if errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("cannot read %s: %v. Check its permissions", StateFile, err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		// A damaged consent record must never switch upload on.
		return State{}, fmt.Errorf("%s is not valid (%v). Delete it, then run `trove contribute enable` again if you still want to share", StateFile, err)
	}
	return s, nil
}

// SaveState writes the record with owner-only permissions.
func SaveState(dir string, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, StateFile+".*")
	if err != nil {
		return fmt.Errorf("cannot write %s in %s: %v. Check that the folder is writable", StateFile, dir, err)
	}
	name := tmp.Name()
	_, werr := tmp.Write(append(b, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(name)
		return fmt.Errorf("cannot write %s: %v", StateFile, errors.Join(werr, cerr))
	}
	_ = os.Chmod(name, 0o600)
	if err := os.Rename(name, filepath.Join(dir, StateFile)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("cannot write %s: %v", StateFile, err)
	}
	return nil
}

// NewInstallID returns 128 random bits as hex.
func NewInstallID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("cannot make a random id: %v", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Due reports whether an automatic upload should happen now.
func (s State) Due(now time.Time) bool {
	if !s.Enabled || s.InstallID == "" {
		return false
	}
	if s.LastSent == "" {
		return true
	}
	last, err := time.Parse(time.DateOnly, s.LastSent)
	if err != nil {
		return true
	}
	return now.UTC().Sub(last) >= Interval
}
