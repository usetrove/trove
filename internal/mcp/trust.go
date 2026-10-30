package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// trustFile lives in the Trove data directory, not in the project, so a cloned
// repository cannot approve itself.
const trustFile = "mcp_trust.json"

// Trust decides whether a project's .trove/commands.toml may be run. A
// repository is untrusted input: without this step, any repo you clone could
// name a program and have a prompt-injected agent run it.
type Trust interface {
	// Approved reports whether the commands file with this content hash was
	// approved by the user for this project folder.
	Approved(project, fileHash string) bool
}

// FileHash is the identifier of one version of a commands file.
func FileHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// TrustStore keeps approvals as hashes only (never project paths) in a file
// with mode 0600. Any edit to the commands file changes its hash and so
// withdraws the approval.
type TrustStore struct {
	path string
	mu   sync.Mutex
}

// OpenTrust returns the approvals kept under dataDir.
func OpenTrust(dataDir string) *TrustStore {
	return &TrustStore{path: filepath.Join(dataDir, trustFile)}
}

func projectKey(project string) string {
	sum := sha256.Sum256([]byte(project))
	return hex.EncodeToString(sum[:16])
}

func (t *TrustStore) load() (map[string]string, error) {
	b, err := os.ReadFile(t.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s is damaged: %v. Delete it and approve again with `trove mcp trust`", t.path, err)
	}
	return m, nil
}

// Approved is false on any error: when in doubt, nothing runs.
func (t *TrustStore) Approved(project, fileHash string) bool {
	if t == nil || fileHash == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	m, err := t.load()
	if err != nil {
		return false
	}
	return m[projectKey(project)] == fileHash
}

// Approve records the user's approval of one version of a project's file.
func (t *TrustStore) Approve(project, fileHash string) error {
	return t.update(func(m map[string]string) { m[projectKey(project)] = fileHash })
}

// Revoke withdraws approval for a project.
func (t *TrustStore) Revoke(project string) error {
	return t.update(func(m map[string]string) { delete(m, projectKey(project)) })
}

func (t *TrustStore) update(fn func(map[string]string)) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, err := t.load()
	if err != nil {
		return err
	}
	fn(m)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(t.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mcp_trust-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // best effort; the rename below consumes it on success
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrInvalid) {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), t.path)
}
