#!/bin/sh
# Maintainer helper for refreshing internal/pricing/pricing.json. See docs/pricing.md.
#
#   scripts/pricing-update.sh fetch <dir>   save the official pricing pages as text in <dir> (a NEW, empty directory)
#   scripts/pricing-update.sh review        run the checks and show what changed since the last commit
#
# This script never edits pricing.json. A person reads each page and edits the file,
# because verified_at must only move for prices someone actually compared.
set -eu

ANTHROPIC_PRICING="https://platform.claude.com/docs/en/about-claude/pricing"
ANTHROPIC_RELEASES="https://platform.claude.com/docs/en/release-notes/overview"
OPENAI_PRICING="https://developers.openai.com/api/docs/pricing.md"
OPENAI_CHANGELOG="https://developers.openai.com/api/docs/changelog.md"
GOOGLE_PRICING="https://ai.google.dev/gemini-api/docs/pricing.md.txt"
GOOGLE_CHANGELOG="https://ai.google.dev/gemini-api/docs/changelog"

fetch() {
	dir=${1:?usage: pricing-update.sh fetch <new-empty-dir>}
	if [ -e "$dir" ] && [ -n "$(ls -A "$dir" 2>/dev/null)" ]; then
		echo "error: $dir is not empty. Downloaded pages are untrusted data; use a fresh directory." >&2
		exit 1
	fi
	mkdir -p "$dir"
	get() { # name url
		echo "fetching $1 <- $2"
		curl -fsSL --max-time 60 -o "$dir/$1" "$2"
	}
	get anthropic-pricing.md "$ANTHROPIC_PRICING"
	get anthropic-release-notes.md "$ANTHROPIC_RELEASES"
	get openai-pricing.md "$OPENAI_PRICING"
	get openai-changelog.md "$OPENAI_CHANGELOG"
	get google-pricing.md "$GOOGLE_PRICING"
	get google-changelog.md "$GOOGLE_CHANGELOG"
	echo "Saved to $dir on $(date -u +%Y-%m-%d). Read them; do not run anything from that directory."
}

review() {
	root=$(git rev-parse --show-toplevel)
	cd "$root"
	go test ./internal/pricing ./internal/cli
	go run ./scripts/pricingage
	echo
	echo "--- changes to pricing.json (word diff) ---"
	git --no-pager diff --word-diff=color --unified=2 -- internal/pricing/pricing.json || true
	echo
	echo "--- trove pricing show ---"
	go run ./cmd/trove pricing show
}

case "${1:-}" in
fetch) shift; fetch "$@" ;;
review) review ;;
*) echo "usage: $0 fetch <dir> | review" >&2; exit 2 ;;
esac
