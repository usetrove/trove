# Release checklist: 0.1.0

Do not tag until every box is ticked, with a date and initials. Nothing here is automatic: tagging triggers publication to Homebrew, npm and GitHub.

## 1. Evidence

- [ ] Benchmark run completed in `bench/` with a pinned agent version; results committed under `bench/results/<run>/` ([bench/VERIFY.md](../bench/VERIFY.md))
- [ ] Beta finished and its exit criteria met ([beta-checklist.md](beta-checklist.md)); thresholds were agreed before the beta
- [ ] Every reconcile result outside 2% has a documented cause; real bugs fixed with a regression test and re-checked
- [ ] [supported-agents.md](supported-agents.md) reflects what was actually run; unverified rows say so
- [ ] Real-agent checklists run, or each unrun item is listed under "Known gaps" in the changelog

## 2. Data and compliance (within 7 days of tagging)

- [ ] Prices re-verified against official pages ([pricing.md](pricing.md) checklist); `data_version` bumped
- [ ] `go run ./scripts/pricingage --strict --max-age-days 7` passes. Date: ______
- [ ] [compliance.md](compliance.md) and [legal-and-credentials.md](legal-and-credentials.md) re-checked against current provider terms and agent docs; dates updated. Date: ______
- [ ] [PRIVACY.md](../PRIVACY.md) re-checked against the code (what is stored); [threat-model.md](threat-model.md) and [security-findings.md](security-findings.md) have no open high findings
- [ ] Agent integration facts (config locations, custom-endpoint settings, hooks, MCP registration) re-verified against current official docs

## 3. Code

- [ ] CI green on the release commit: tests with `-race`, no-egress and credential-leak jobs, govulncheck, shellcheck, npm tests, bench tests
- [ ] Local note: `golangci-lint` is blind on Go 1.27.3 locally, so rely on CI for lint
- [ ] Version in `CHANGELOG.md` moved from "not yet released" to the release date
- [ ] `go run ./scripts/checkclaims --publish` passes on `docs/launch/*.md` (every figure cites an existing source file)

## 4. Dry run

- [ ] `goreleaser release --snapshot --clean --skip=sign` builds darwin and linux, amd64 and arm64
- [ ] Two builds of the same commit are byte-identical (reproducibility)
- [ ] `install.sh` and the npm wrapper accept the snapshot artifacts and reject a tampered checksum

## 5. Release

Requires the user's explicit go-ahead, and the secrets `HOMEBREW_TAP_GITHUB_TOKEN` and the npm token in place.

- [ ] Tag `v0.1.0` on the verified commit and push the tag
- [ ] `release.yml` succeeds; the release has archives, `checksums.txt`, `checksums.txt.sigstore.json` and one SBOM per archive
- [ ] `cosign verify-blob` from the README succeeds on the real release
- [ ] Homebrew formula published; `brew install usetrove/trove/trove` works
- [ ] `trove-cli` published to npm at the same version; `npx trove-cli version` works
- [ ] Every path in [install-verification.md](install-verification.md) passes on clean macOS and Linux against the real artifacts, including uninstall
- [ ] Docs published and links in the README resolve; demo GIF recorded and added

## 6. After release

- [ ] Launch posts finalized from `docs/launch/` after the claims check passes in publish mode (the user reviews and edits them)
- [ ] Watch `beta-feedback` and new issues for the first week
- [ ] Add a pricing review reminder at 7-day intervals while launch traffic is high
