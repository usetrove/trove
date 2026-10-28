#!/bin/sh
# Tests install.sh against a local file:// "release". No network needed.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

sum() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{ print $1 }'; else shasum -a 256 "$1" | awk '{ print $1 }'; fi
}

export TROVE_OS=linux TROVE_ARCH=amd64 TROVE_VERSION=1.0.0
rel="$work/rel/v1.0.0"
mkdir -p "$rel" "$work/src"
printf '#!/bin/sh\necho fake-trove\n' >"$work/src/trove"
file=trove_1.0.0_linux_amd64.tar.gz
tar -czf "$rel/$file" -C "$work/src" trove
# Git Bash on Windows hands native curl a C:/ path; everywhere else pwd is already right.
wp=$(cd "$work" && { pwd -W 2>/dev/null || pwd; })
case "$wp" in [A-Za-z]:*) export TROVE_BASE_URL="file:///$wp/rel" ;; *) export TROVE_BASE_URL="file://$wp/rel" ;; esac

failures=0
check() { # name, condition-exit-status
  if [ "$2" -eq 0 ]; then echo "ok   - $1"; else echo "FAIL - $1"; failures=$((failures + 1)); fi
}

# 1. good checksum installs and the binary runs
echo "$(sum "$rel/$file")  $file" >"$rel/checksums.txt"
rc=0
TROVE_INSTALL_DIR="$work/bin1" sh "$here/install.sh" >"$work/out1" 2>&1 || rc=$?
check "installs with a correct checksum" $rc
[ "$rc" -eq 0 ] || cat "$work/out1"
[ -x "$work/bin1/trove" ] && [ "$("$work/bin1/trove")" = fake-trove ]
check "installed binary is executable" $?

# 2. wrong checksum is refused and nothing is installed
echo "$(printf '0%.0s' $(seq 1 64))  $file" >"$rel/checksums.txt"
if TROVE_INSTALL_DIR="$work/bin2" sh "$here/install.sh" >"$work/out2" 2>&1; then rc=1; else rc=0; fi
check "refuses a checksum mismatch" $rc
grep -q "checksum mismatch" "$work/out2"
check "mismatch message is plain English" $?
[ ! -e "$work/bin2/trove" ]
check "nothing installed after a mismatch" $?

# 3. missing checksum entry is refused
echo "$(sum "$rel/$file")  other.tar.gz" >"$rel/checksums.txt"
if TROVE_INSTALL_DIR="$work/bin3" sh "$here/install.sh" >/dev/null 2>&1; then rc=1; else rc=0; fi
check "refuses a missing checksum entry" $rc

# 4. unsupported OS is refused
if TROVE_OS=plan9 TROVE_INSTALL_DIR="$work/bin4" sh "$here/install.sh" >"$work/out4" 2>&1; then rc=1; else rc=0; fi
check "refuses an unsupported OS" $rc

[ "$failures" -eq 0 ] || { echo "$failures failure(s)"; exit 1; }
echo "all install tests passed"
