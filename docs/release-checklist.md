# Release checklist: 0.1.0 (beta first)

Do not tag until every box required for the release is ticked, with a date and initials. Nothing here is automatic: tagging triggers `release.yml`, which publishes the GitHub release and the Homebrew formula. **There is no npm package in the beta** (decided 2026-11-07; a scoped package comes after launch). See "Beta triage" below for which open boxes gate the beta and which can wait.

## 1. Evidence

- [ ] DEFERRED for the beta: benchmark run completed in `bench/` with a pinned agent version; results committed under `bench/results/<run>/` ([bench/VERIFY.md](../bench/VERIFY.md)). No savings claim is made in the beta.
- [ ] Beta finished and its exit criteria met ([beta-checklist.md](beta-checklist.md)); thresholds were agreed before the beta
- [ ] Every reconcile result outside 2% has a documented cause; real bugs fixed with a regression test and re-checked
- [ ] [supported-agents.md](supported-agents.md) reflects what was actually run; unverified rows say so
- [ ] Real-agent checklists run, or each unrun item is listed under "Known gaps" in the changelog

## 2. Data and compliance (within 7 days of tagging)

- [ ] Prices re-verified against official pages ([pricing.md](pricing.md) checklist); `data_version` bumped
- [ ] `go run ./scripts/pricingage --strict --max-age-days 7` passes. Date: ______
- [x] OpenAI policy pages reviewed manually, 2026-11-07, by Jaclyn Sunardi. Initials: ______. Note: the pages returned HTTP 403 to automated reads, so this review was done by hand from the terms, not from a fetched copy.
- [ ] [compliance.md](compliance.md) and [legal-and-credentials.md](legal-and-credentials.md) re-checked against current Anthropic provider terms and agent docs; dates updated. Last done 2026-11-06. Date: ______
- [ ] [data.md](data.md) matches the code (`go test ./internal/store` fails if not); [PRIVACY.md](../PRIVACY.md) re-checked against the code (what is stored); [threat-model.md](threat-model.md) and [security-findings.md](security-findings.md) have no open high findings
- [ ] Agent integration facts (config locations, custom-endpoint settings, hooks, MCP registration) re-verified against current official docs

## 3. Code

- [ ] CI green on the release commit: tests with `-race`, no-egress and credential-leak jobs, govulncheck, shellcheck, bench tests, and the install and plugin-hook smoke test (`.github/workflows/beta-smoke.yml`)
- [ ] Local note: `golangci-lint` is blind on Go 1.27.3 locally, so rely on CI for lint
- [ ] Version in `CHANGELOG.md` moved from "not yet released" to the release date
- [ ] `go run ./scripts/checkclaims --publish` passes on `docs/launch/*.md` (every figure cites an existing source file)
- [ ] Plugin: `plugin/trove/VERSION` set to the release version (no `v`, same as the archive names) and committed to the default branch before tagging. The marketplace installs from the default branch, and `release.yml` fails the release if VERSION does not match the tag. `claude plugin validate plugin/trove --strict` passes; [plugin-verification.md](plugin-verification.md) run on a real login on macOS or Linux

## 4. Dry run

- [ ] `goreleaser release --snapshot --clean --skip=sign` builds darwin, linux and windows, amd64 and arm64
- [ ] Two builds of the same commit are byte-identical (reproducibility)
- [ ] `install.sh` accepts the snapshot artifacts and rejects a tampered checksum; the Windows zip passes the checksum steps in the README

## 5. Release

Requires the user's explicit go-ahead, and the secret `HOMEBREW_TAP_GITHUB_TOKEN` in place.

- [ ] Tag `v0.1.0-beta.1` on the verified commit and push the tag. The `-beta` suffix makes it a pre-release, so `install.sh` and the release page's "latest" are not moved.
- [ ] `release.yml` succeeds; the release has archives (tar.gz, and zip for Windows), `checksums.txt`, `checksums.txt.sigstore.json` and one SBOM per archive
- [ ] `cosign verify-blob` from the README succeeds on the real release
- [ ] Homebrew formula published; `brew install usetrove/trove/trove` works
- [ ] Every path in [install-verification.md](install-verification.md) passes on clean macOS and Linux against the real artifacts, including uninstall
- [ ] Docs published and links in the README resolve; demo GIF recorded and added

## 6. After release

- [ ] Launch posts finalized from `docs/launch/` after the claims check passes in publish mode (the user reviews and edits them)
- [ ] Watch `beta-feedback` and new issues for the first week
- [ ] Add a pricing review reminder at 7-day intervals while launch traffic is high

## Beta triage (2026-11-07)

The open boxes above, sorted. "Required" means the beta must not ship without it; "deferrable" means it can wait for 0.1.0 final or after launch.

**Required for a safe beta**

- Supported-agents table matches what was run, so no support is claimed that was not tested (section 1).
- Real-agent gaps listed under "Known gaps" in the changelog, so users are told what is unverified (section 1).
- Prices re-verified and `pricingage --strict` passes, so dollar estimates are not built on stale prices (section 2).
- Compliance, legal and data/privacy docs match the code, because they make promises users rely on (section 2).
- Agent integration facts (config locations, hooks, MCP) re-checked, because the install writes to those files (section 2).
- CI green on the release commit, including the new beta smoke test (section 3).
- Plugin VERSION committed to the default branch and validated, plus a real plugin login on macOS or Linux, because the plugin is the main way in (section 3).
- Snapshot builds, install.sh checksum behaviour, and the Windows checksum steps checked (section 4).
- Tag, release workflow, cosign verification and Homebrew install all work, since the README promises them (section 5).
- Install verification on clean macOS and Linux, because those are the supported platforms (section 5).
- Changelog date and the "latest" release handling, so the beta does not replace a stable release (section 3 and 5).

**Deferrable**

- Benchmark run: no savings claim is made in the beta, so there is nothing to back with it yet.
- Beta finished and exit criteria: that gates 0.1.0 final, not a beta tag.
- Reconcile results outside 2%: there is no invoice data yet, and dollar figures are labelled as estimates.
- Local golangci-lint note: this is information, not an action; CI runs lint.
- Byte-identical reproducibility check: a nice property, not a claim the beta depends on.
- `checkclaims --publish` for launch drafts: launch posts are not part of the beta.
- Demo GIF: a launch asset, not needed to install or use the beta.
- Launch posts, feedback watch and pricing reminders: these happen after the release.

## Status as of 2026-10-30 (notes only)

Verified then, by command (not a substitute for the dated, initialed boxes above):

- `go run ./scripts/pricingage --strict --max-age-days 7` passes (54 prices, data version 2026-10-28.3). This checks the recorded dates only; the prices were not re-verified against the official pages then.
- `goreleaser release --snapshot --clean --skip=sign` builds darwin and linux for amd64 and arm64, and produces one SPDX 2.3 SBOM per archive (syft). Two builds of one commit gave byte-identical archives.
- `sh install_test.sh` passes, including the refusals for a checksum mismatch and a missing checksum entry. These use test fixtures, not the snapshot archives.
- CI was green on `main` at that commit.

Still open, and not something code can close:

- **Evidence:** the `bench/` run, the beta, and the `docs/launch/*` slots all need real measurements (`go run ./scripts/checkclaims --publish` reports unresolved slots). Nothing here should be filled in with estimates.
- **Local tooling:** on this Windows machine `go test -race` fails without cgo, and two tests need Linux or macOS. CI covers them.
