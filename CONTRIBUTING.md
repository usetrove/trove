# Contributing

Read [CLAUDE.md](CLAUDE.md) first: it holds the non-negotiable rules (local-first, credentials never logged, fail open, metadata only).

## Workflow
- Small, reviewable commits, each with tests.
- Table-driven tests; golden files for parsers and reports; recorded and redacted fixtures for API traffic. No live API calls in tests or CI.
- Pure-Go dependencies only (no cgo).
- Run `gofmt`, `golangci-lint run`, `go test ./...` and `govulncheck ./...` before opening a PR.
- Never commit real API keys or unredacted traffic. Route all logging and error output through `internal/redact`.
- For provider or agent behavior, cite the official documentation you checked and the date.

## Security issues
Do not open a public issue; see [SECURITY.md](SECURITY.md).
