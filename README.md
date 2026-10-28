# Trove

The efficiency layer for AI coding agents. Trove is a local, open-source tool (a single Go binary) that cuts the tokens your coding agents waste and shows that results did not suffer.

> Status: foundation only. `trove version` and `trove doctor` work; the other commands print "not implemented yet".

## Principles
Local-first (everything lives in `~/.trove`, no telemetry), API keys are never stored or logged, every optimization is opt-in with a shadow mode, and every number is labeled measured or estimated. See [CLAUDE.md](CLAUDE.md).

## Install (once a release exists)
```sh
curl -fsSL https://raw.githubusercontent.com/jaclynsunardi/trove/main/install.sh | sh   # verifies the SHA-256 checksum
npx trove-cli doctor                                                                     # downloads and verifies the binary
brew install jaclynsunardi/trove/trove
```
Releases also carry a cosign signature over `checksums.txt` and an SBOM per archive. To verify the signature:
```sh
cosign verify-blob \
  --certificate-identity 'https://github.com/jaclynsunardi/trove/.github/workflows/release.yml@refs/tags/vX.Y.Z' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --bundle checksums.txt.sigstore.json checksums.txt
```

## Build and test
Requires the current stable Go (govulncheck flags older patch releases of the standard library).

```sh
go build -o trove ./cmd/trove   # one static binary, no cgo
go test ./...                   # unit tests
go test -race ./...             # race detector (needs cgo; CI runs it on Linux and macOS)
golangci-lint run               # lint (v2 config in .golangci.yml)
govulncheck ./...               # known-vulnerability scan
node --test "npm/test/*.test.js"  # npm wrapper tests (Node 22+)
sh install_test.sh              # install script tests (no network)
./trove version
./trove doctor
./trove doctor --json
```

Use `--data-dir <path>` (or `TROVE_DATA_DIR`) to keep experiments out of `~/.trove`.

## Configuration
`~/.trove/config.toml` is optional; defaults are documented in `internal/config` (`DefaultTOML`). Any setting can be overridden with `TROVE_` variables, for example `TROVE_GATEWAY_PORT=9000`.

## Release dry run
```sh
goreleaser release --snapshot --clean --skip=sign          # add ,sbom if syft is not installed
```
Real releases run in GitHub Actions on a `v*` tag (`.github/workflows/release.yml`) and need a `HOMEBREW_TAP_GITHUB_TOKEN` secret with write access to `jaclynsunardi/homebrew-trove`. The `trove-cli` npm package version is set to the release version at publish time.

## License
Apache-2.0
