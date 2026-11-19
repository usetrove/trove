## What and why

<!-- A short description of the change and the problem it solves. -->

## Checklist

- [ ] Small, reviewable commits, each with tests (table-driven; golden files for parsers and reports)
- [ ] `gofmt`, `golangci-lint run`, `go test ./...` and `govulncheck ./...` pass
- [ ] No live API calls in tests, no real keys or unredacted traffic committed
- [ ] Logging and error output go through `internal/redact`
- [ ] Any provider or agent behavior cites the official documentation checked, and the date
- [ ] Any number shown to users is labeled **measured** or **estimated**
- [ ] I read [PRINCIPLES.md](../PRINCIPLES.md): local-first, credentials never logged, metadata only, fail open
