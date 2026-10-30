// Package root confines file access to one project directory. Every path an
// MCP tool receives goes through Resolve; nothing else opens files.
package root

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Root is a project directory with its symlinks resolved.
type Root struct{ real string }

// New resolves dir and returns a Root for it.
func New(dir string) (*Root, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("cannot open the project folder %s: %w. Start the agent from inside the project", dir, err)
	}
	return &Root{real: real}, nil
}

// Dir is the resolved project directory.
func (r *Root) Dir() string { return r.real }

// ErrOutside is returned when a path leaves the project.
var ErrOutside = errors.New("path is outside the project folder; only files inside the project can be read")

// Resolve turns a project-relative path into a real path inside the project. It
// refuses absolute paths, "..", and any path (or symlink along it) that leads
// outside. The path need not exist yet only when mustExist is false.
func (r *Root) Resolve(rel string) (string, error) {
	if rel == "" {
		return "", errors.New("path is empty")
	}
	if strings.ContainsRune(rel, 0) {
		return "", errors.New("path contains an invalid character")
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", ErrOutside
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrOutside
	}
	real, err := filepath.EvalSymlinks(filepath.Join(r.real, clean))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%s does not exist in the project", rel)
		}
		return "", fmt.Errorf("cannot read %s: %w", rel, err)
	}
	if !r.Contains(real) {
		return "", ErrOutside
	}
	return real, nil
}

// Contains reports whether an already-resolved path is inside the project.
func (r *Root) Contains(real string) bool {
	rel, err := filepath.Rel(r.real, real)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// Rel returns the project-relative, slash-separated form of a resolved path.
func (r *Root) Rel(real string) string {
	rel, err := filepath.Rel(r.real, real)
	if err != nil {
		return real
	}
	return filepath.ToSlash(rel)
}

// OpenRead resolves rel and opens it, then re-checks that the opened file is
// the one that was resolved, which narrows the window for a symlink swapped in
// between the check and the open.
func (r *Root) OpenRead(rel string) (*os.File, os.FileInfo, error) {
	real, err := r.Resolve(rel)
	if err != nil {
		return nil, nil, err
	}
	// Check the name asked for and the real name behind any symlink, so a link
	// called notes.txt that points at .env is refused too.
	if Sensitive(rel) || Sensitive(r.Rel(real)) {
		return nil, nil, ErrSensitive
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read %s: %w", rel, err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	again, err := r.Resolve(rel)
	if err != nil || again != real {
		_ = f.Close()
		return nil, nil, ErrOutside
	}
	if st, err := os.Stat(real); err != nil || !os.SameFile(fi, st) {
		_ = f.Close()
		return nil, nil, ErrOutside
	}
	return f, fi, nil
}
