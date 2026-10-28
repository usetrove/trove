# Trove

The efficiency layer for AI coding agents. Trove is a local, open-source tool (a single Go binary) that cuts the tokens your coding agents waste and shows that results did not suffer.

> Status: foundation only. Most commands print "not implemented yet".

## Principles
Local-first (everything lives in `~/.trove`, no telemetry), API keys are never stored or logged, every optimization is opt-in with a shadow mode, and every number is labeled measured or estimated. See [CLAUDE.md](CLAUDE.md).

## Build and test
Requires the current stable Go.

```sh
go build -o trove ./cmd/trove   # one static binary, no cgo
go test ./...                   # unit tests
go test -race ./...             # race detector (needs cgo; CI runs it on Linux)
./trove version
./trove doctor
./trove doctor --json
```

Use `--data-dir <path>` (or `TROVE_DATA_DIR`) to keep experiments out of `~/.trove`.

## Release dry run
```sh
goreleaser release --snapshot --clean --skip=sign
```

## License
Apache-2.0
