package measure

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CommitCount returns how many commits in the repository at dir have a commit
// date in [from, to]. It is read-only (git rev-list) and returns only a number:
// no messages, file names or diffs are ever read.
func CommitCount(ctx context.Context, dir string, from, to time.Time) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-list", "--count", "--all", //nolint:gosec // dir is the user's own configured repo
		"--since="+from.UTC().Format(time.RFC3339), "--until="+to.UTC().Format(time.RFC3339))
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0") // never take a lock in the user's repo
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("could not count commits in the configured git_repo: %v. Check that measure.git_repo is a git repository and git is installed", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("git returned an unexpected answer when counting commits")
	}
	return n, nil
}
