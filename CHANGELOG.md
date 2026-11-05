# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

Preparing 0.1.0, the first public release. See [docs/release-checklist.md](docs/release-checklist.md).

### Added
- `trove reconcile`: compare Trove's recorded total for a period with a provider invoice; a difference within 2% (adjustable) counts as a match.
- `trove feedback`: build a prefilled GitHub issue with diagnostics (versions, agent names, counts, error classes), shown in full before anything is opened.
- `trove doctor` warns when bundled prices were last verified more than 30 days ago.
- `scripts/pricingage --strict --max-age-days N`: release gate for price freshness.
- `scripts/checkclaims`: fails if a launch draft states a figure without a measured or estimated slot that cites a real source file.
- README rewrite and new docs: measurement method, supported agents, beta checklist, install verification, release checklist, launch drafts.
- Claude Code plugin (`plugin/trove`, marketplace in `.claude-plugin`): hooks, `/trove:report`, `/trove:status`, `/trove:setup`, `/trove:uninstall`, a first-run report, and a capability check after Claude Code updates. Works with every login type; nothing is enabled without approval. New `trove plugin start|status|status-line`. `trove hooks install` refuses to double-register when the plugin is enabled. See [docs/plugin.md](docs/plugin.md). No `/trove:handoff` yet.

## [0.1.0] - not yet released

The first release will include everything below. Nothing has been published.

### Added
- Local gateway (opt-in, `127.0.0.1`) for API-key use with Claude Code and Codex CLI; `trove setup` and `trove undo` with backups.
- Request analysis: token estimates, cache-miss reasons, waste detectors, shadow savings.
- Optimizers (all off by default, shadow mode first, automatic rollback by guardrails): `noise_normalizer`, `duplicate_result_collapser`, `cache_breakpoints`, `volatile_prefix_mover`, `output_trimmer`; suggestions for model choice, Batch API and reasoning effort.
- Measurement, reports, budgets, receipts, status line and share card.
- History audit: reads Claude Code and Codex CLI session history without the gateway, and detects API-key or subscription plans without reading credentials.
- `trove fix`: `mcp_prune`, `context_slim`, `cache_breaker_fix`, `model_default`, with exact diffs and per-fix undo.
- MCP server (`trove_read`, `trove_search`, `trove_run`, `trove_fetch_full`) with a path jail and a local recovery store.
- Agent hooks (repeat-read prevention, session receipts) and `trove hooks`.
- `trove uninstall` with byte-exact restore and optional data purge.
- Pricing engine with verified, dated price data and `trove pricing show`.
- Security work: key-leakage and no-egress tests, threat model, privacy statement, compliance checklist and findings report.
- Reproducible builds; cosign-signed checksums and SBOMs; Homebrew, npm and install-script distribution.

### Known gaps
- No benchmark run has been completed, so no savings claims are made.
- Real-agent verification checklists are still pending for most features; see [docs/supported-agents.md](docs/supported-agents.md).
- Codex CLI history parsing has not been verified on a real install.
- macOS and Linux have not yet been tested by hand; automated tests so far ran on Windows and in CI.
- `trove config get|set` is not implemented.
