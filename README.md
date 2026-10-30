# Trove

The efficiency layer for AI coding agents. Trove is a local, open-source tool (a single Go binary) that cuts the tokens your coding agents waste and shows that results did not suffer.

> Status: pre-release. The gateway, analysis, optimizers, measurement, reports, fixes, MCP server, hooks and `trove uninstall` are implemented. Only `trove config get|set` still prints "not implemented yet". Nothing has been released yet.

## Principles
Local-first (everything lives in `~/.trove`, no telemetry), API keys are never stored or logged, every optimization is opt-in with a shadow mode, and every number is labeled measured or estimated. See [CLAUDE.md](CLAUDE.md).

Security and privacy: [PRIVACY.md](PRIVACY.md) (exactly what is stored and where), [docs/threat-model.md](docs/threat-model.md), [docs/security-findings.md](docs/security-findings.md) and [docs/compliance.md](docs/compliance.md). To remove everything Trove changed, run `trove uninstall` (add `--purge-data` to delete what it stored).

## Install (once a release exists)
```sh
curl -fsSL https://raw.githubusercontent.com/usetrove/trove/main/install.sh | sh   # checks the archive's SHA-256 against checksums.txt (not the signature)
npx trove-cli doctor                                                                     # same checksum check before it runs the binary
brew install usetrove/trove/trove
```
Releases also carry a cosign signature over `checksums.txt` and an SBOM per archive. To verify the signature:
```sh
cosign verify-blob \
  --certificate-identity 'https://github.com/usetrove/trove/.github/workflows/release.yml@refs/tags/vX.Y.Z' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --bundle checksums.txt.sigstore.json checksums.txt
```

## Build and test
Requires Go 1.27.3 or newer (`go.mod` says so; older patch releases of the standard library have known vulnerabilities that govulncheck flags).

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
Real releases run in GitHub Actions on a `v*` tag (`.github/workflows/release.yml`) and need a `HOMEBREW_TAP_GITHUB_TOKEN` secret with write access to `usetrove/homebrew-trove`. The `trove-cli` npm package version is set to the release version at publish time.

## License
Apache-2.0
