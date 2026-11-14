#!/bin/sh
# Trove installer: downloads a release archive, verifies its SHA-256 against the
# release's checksums.txt, and installs the binary. It refuses to install on a mismatch.
#
#   curl -fsSL https://raw.githubusercontent.com/usetrove/trove/main/install.sh | sh
#
# Environment:
#   TROVE_VERSION      version to install, e.g. 1.2.3 (default: latest release)
#   TROVE_INSTALL_DIR  where to put the binary (default: $HOME/.local/bin)
#   TROVE_BASE_URL     release download base (default: GitHub Releases); for testing
#   TROVE_COSIGN       cosign command to use (default: cosign); for testing
#   TROVE_REQUIRE_SIGNATURE  set to 1 to refuse to install unless cosign verifies the release signature
#   TROVE_OS, TROVE_ARCH  override platform detection (darwin|linux, amd64|arm64); for testing
set -eu

REPO="usetrove/trove"
BASE="${TROVE_BASE_URL:-https://github.com/$REPO/releases/download}"
DEST="${TROVE_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
  echo "trove install: $*" >&2
  exit 1
}

os="${TROVE_OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
case "$os" in
  darwin | linux) ;;
  *) fail "unsupported OS '$os'. Supported: macOS and Linux. Build from source: go install github.com/$REPO/cmd/trove@latest" ;;
esac

arch="${TROVE_ARCH:-$(uname -m)}"
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported CPU '$arch'. Supported: x86_64 and arm64. Build from source: go install github.com/$REPO/cmd/trove@latest" ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required. Install curl and run this again."

version="${TROVE_VERSION:-}"
if [ -z "$version" ]; then
  # The /releases/latest page redirects to /releases/tag/vX.Y.Z; read that without the API.
  loc=$(curl -fsSI -o /dev/null -w '%{url_effective}' -L "https://github.com/$REPO/releases/latest") ||
    fail "could not look up the latest release. Check your network, or set TROVE_VERSION."
  version="${loc##*/v}"
fi
version="${version#v}"
[ -n "$version" ] || fail "could not work out the version to install. Set TROVE_VERSION."

file="trove_${version}_${os}_${arch}.tar.gz"
tmp=$(mktemp -d) || fail "could not create a temporary folder."
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading Trove $version for $os/$arch..."
curl -fsSL -o "$tmp/$file" "$BASE/v$version/$file" ||
  fail "download failed for $BASE/v$version/$file. Check your network and that version $version exists."
curl -fsSL -o "$tmp/checksums.txt" "$BASE/v$version/checksums.txt" ||
  fail "could not download checksums.txt for version $version."

# The checksum file is signed (keyless cosign, by this repository's release workflow). A checksum from the same
# place as the archive only catches corruption; the signature is what catches a tampered release. If cosign is
# installed it is checked and a bad signature stops the install. Without cosign the install goes ahead on the
# checksum alone and says so, unless TROVE_REQUIRE_SIGNATURE=1, which refuses to install unverified.
signature="checksums verified, signature not checked"
have_bundle=0
curl -fsSL -o "$tmp/checksums.txt.sigstore.json" "$BASE/v$version/checksums.txt.sigstore.json" 2>/dev/null && have_bundle=1
cosign_cmd="${TROVE_COSIGN:-cosign}"
if command -v "$cosign_cmd" >/dev/null 2>&1 && [ "$have_bundle" -eq 1 ]; then
  "$cosign_cmd" verify-blob \
    --certificate-identity "https://github.com/$REPO/.github/workflows/release.yml@refs/tags/v$version" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
    --bundle "$tmp/checksums.txt.sigstore.json" "$tmp/checksums.txt" >/dev/null 2>&1 ||
    fail "the signature on checksums.txt for version $version did not verify, so nothing was installed. The release may have been tampered with; report it at https://github.com/$REPO/security."
  signature="checksum and signature verified"
elif [ "${TROVE_REQUIRE_SIGNATURE:-}" = "1" ]; then
  fail "TROVE_REQUIRE_SIGNATURE=1 is set but the release signature cannot be checked (cosign is not installed, or the release has no signature file), so nothing was installed. Install cosign (https://docs.sigstore.dev) and run this again."
else
  echo "Note: cosign is not installed, so the release signature was not checked, only the checksum. Install cosign and run this again to check it, or set TROVE_REQUIRE_SIGNATURE=1 to refuse unverified installs." >&2
fi

want=$(awk -v f="$file" '$2 == f || $2 == "*" f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "checksums.txt has no entry for $file. The release may be incomplete."

if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$file" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "$tmp/$file" | awk '{ print $1 }')
else
  fail "need sha256sum or shasum to verify the download. Install one and run this again."
fi

if [ "$got" != "$want" ]; then
  fail "checksum mismatch for $file (expected $want, got $got). The download was corrupted or tampered with, so nothing was installed."
fi

tar -xzf "$tmp/$file" -C "$tmp" trove || fail "could not unpack $file."
mkdir -p "$DEST" || fail "could not create $DEST. Set TROVE_INSTALL_DIR to a writable folder."
cp "$tmp/trove" "$DEST/trove" || fail "could not write $DEST/trove. Set TROVE_INSTALL_DIR to a writable folder."
chmod 755 "$DEST/trove"

echo "Installed $DEST/trove ($signature)."
case ":$PATH:" in
  *":$DEST:"*) ;;
  *) echo "Add $DEST to your PATH to run 'trove'." ;;
esac
