# Release checklist: 0.1.0

Do not tag until every box is ticked, with a date and initials. Nothing here is automatic: tagging triggers `release.yml`, which publishes the GitHub release and the Homebrew formula. **It does not publish to npm** (see "Status" below), so the npm steps are manual until a publish job exists.

## 1. Evidence

- [ ] Benchmark run completed in `bench/` with a pinned agent version; results committed under `bench/results/<run>/` ([bench/VERIFY.md](../bench/VERIFY.md))
- [ ] Beta finished and its exit criteria met ([beta-checklist.md](beta-checklist.md)); thresholds were agreed before the beta
- [ ] Every reconcile result outside 2% has a documented cause; real bugs fixed with a regression test and re-checked
- [ ] [supported-agents.md](supported-agents.md) reflects what was actually run; unverified rows say so
- [ ] Real-agent checklists run, or each unrun item is listed under "Known gaps" in the changelog

## 2. Data and compliance (within 7 days of tagging)

- [ ] Prices re-verified against official pages ([pricing.md](pricing.md) checklist); `data_version` bumped
- [ ] `go run ./scripts/pricingage --strict --max-age-days 7` passes. Date: ______
- [ ] [compliance.md](compliance.md) and [legal-and-credentials.md](legal-and-credentials.md) re-checked against current provider terms and agent docs; dates updated. Last done 2026-11-06 (the OpenAI pages could not be fetched and still need a manual read). Date: ______
- [ ] [data.md](data.md) matches the code (`go test ./internal/store` fails if not); [PRIVACY.md](../PRIVACY.md) re-checked against the code (what is stored); [threat-model.md](threat-model.md) and [security-findings.md](security-findings.md) have no open high findings
- [ ] Agent integration facts (config locations, custom-endpoint settings, hooks, MCP registration) re-verified against current official docs

## 3. Code

- [ ] CI green on the release commit: tests with `-race`, no-egress and credential-leak jobs, govulncheck, shellcheck, npm tests, bench tests
- [ ] Local note: `golangci-lint` is blind on Go 1.27.3 locally, so rely on CI for lint
- [ ] Version in `CHANGELOG.md` moved from "not yet released" to the release date
- [ ] `go run ./scripts/checkclaims --publish` passes on `docs/launch/*.md` (every figure cites an existing source file)
- [ ] Plugin: `plugin/trove/VERSION` set to the release version (no `v`, same as the archive names) before tagging, so the wrapper downloads the matching program; `claude plugin validate plugin/trove --strict` passes; [plugin-verification.md](plugin-verification.md) run on a real login

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

## Status as of 2026-10-30 (notes only; no box above is ticked or initialed)

Verified today, by command (not a substitute for the dated, initialed boxes above):

- `go run ./scripts/pricingage --strict --max-age-days 7` passes (54 prices, data version 2026-10-28.3). This checks the recorded dates only; the prices were not re-verified against the official pages today.
- `goreleaser release --snapshot --clean --skip=sign` builds darwin and linux for amd64 and arm64, and produces one SPDX 2.3 SBOM per archive (syft). Two builds of one commit gave byte-identical archives.
- `sh install_test.sh` passes, including the refusals for a checksum mismatch and a missing checksum entry. These use test fixtures, not the snapshot archives.
- CI is green on `main` at `4dc4a77` (lint, tests on macOS and Ubuntu, privacy, vulncheck, bench, install-script, npm-wrapper, builds).
- Compliance sources were re-read once; see "Second pass" in [compliance.md](compliance.md). The OpenAI policy pages are still unread (HTTP 403), so section 2's compliance box stays open.

Open, and not something code can close:

- **npm has no publish job.** `release.yml` has no npm step and no npm secret; `npm/package.json` is at version `0.0.0`. Decide how to publish (a token in repo secrets, or npm trusted publishing using the workflow's existing `id-token: write`), and set the version from the tag when publishing. `npm view trove-cli` reports "Unpublished on 2025-04-04": the name was published and removed by someone before, so check on npmjs.com that it can be claimed before relying on it.
- **Evidence:** the `bench/` run, the beta, and the `docs/launch/*` slots all need real measurements (`go run ./scripts/checkclaims --publish` currently reports 13 unresolved slots). Nothing here should be filled in with estimates.
- **Changelog date:** move 0.1.0 out of "not yet released" only when the tag is cut.
- **Local tooling:** on this Windows machine `go install` of syft and `go test -race` fail in cgo; syft installs with `CGO_ENABLED=0`. Two npm wrapper tests fail locally because Git Bash's `tar` cannot read `C:\` paths; they pass in CI.
