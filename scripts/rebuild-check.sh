#!/bin/sh
# Build the release binary twice, from two different directories, and fail unless the bytes match.
# This is the check behind the "deterministic build" claim in docs/security.md (finding F-14).
# It uses the same flags as .goreleaser.yaml, with a fixed version, commit and date.
set -eu
here=$(pwd)
tmp=$(mktemp -d)
trap 'git worktree remove --force "$tmp/second" 2>/dev/null || true; rm -rf "$tmp"' EXIT
commit=$(git rev-parse HEAD)
build() { # dir out
  (cd "$1" && CGO_ENABLED=0 GOOS="${GOOS:-linux}" GOARCH="${GOARCH:-amd64}" go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/usetrove/trove/internal/cli.Version=0.0.0-rebuild -X github.com/usetrove/trove/internal/cli.Commit=$commit -X github.com/usetrove/trove/internal/cli.Date=2000-01-01T00:00:00Z" \
    -o "$2" ./cmd/trove)
}
build "$here" "$tmp/first"
git worktree add --detach -q "$tmp/second" HEAD
# Compare the working tree only if it is clean; otherwise the two builds differ by design.
if [ -n "$(git status --porcelain)" ]; then
  echo "rebuild-check: the working tree has uncommitted changes, so a build of HEAD cannot match it. Commit or stash first." >&2
  exit 2
fi
build "$tmp/second" "$tmp/second-bin"
a=$(sha256sum "$tmp/first" | cut -d' ' -f1)
b=$(sha256sum "$tmp/second-bin" | cut -d' ' -f1)
echo "first:  $a"
echo "second: $b"
if [ "$a" != "$b" ]; then
  echo "rebuild-check: the two builds differ. The release is not reproducible; see docs/security.md (F-14)." >&2
  exit 1
fi
echo "rebuild-check: identical"
