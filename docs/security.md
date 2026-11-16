# Security

Written 2026-11-14. This is the one place that lists every security control in Trove, what each one protects, what it
does not, and the test that keeps it in place. It does not repeat the reasoning in the other three documents:

- [threat-model.md](threat-model.md): the attackers considered and the residual risks (R1 to R8).
- [../PRIVACY.md](../PRIVACY.md) and [data.md](data.md): what is stored, field by field.

How to report a problem, and how to check a release, is in [../SECURITY.md](../SECURITY.md).

Every row names a test. A row with no test says so. If a control changes and this page does not, that is a bug.

## What Trove protects

| Asset | The promise |
|---|---|
| Your API keys | Pass through memory untouched, are never logged, stored or printed (CLAUDE.md rule 2) |
| Your prompts, code and tool output | May be processed in memory; never written to the database (rule 7). The one exception is the opt-in local recovery store |
| Your subscription and OAuth credentials | Never handled. The gateway refuses every login that is not an API key (rules 3, 10) |
| Your machine | Trove runs nothing the user did not approve, writes only its own entries into your agent files, and can restore every file byte for byte |
| Your agent sessions | Trove fails open: any internal error forwards the original request or lets the tool call through (rule 4) |

## What Trove does not protect against

Be clear about the boundary. Trove trusts the operating system account.

- A process already running as you can read your keys, edit your config and replace the binary. Trove does not try to
  stop it (threat model, trust assumptions and R5).
- A provider you point Trove at receives your key, because that is what the key is for. Upstream URLs are validated for
  shape, not restricted to known hosts, because enterprise endpoints are supported.
- Secret detection is by name and by well-known key shape. A secret in an ordinary variable or file is not caught
  (R6).
- Another user on the same machine can bind the gateway's port before Trove does (R1, F-13). Use a different
  `gateway.port` on a shared machine.
- Windows is not a supported platform yet: mode bits mean little and Trove sets no ACLs (R7).
- Nothing here is a guarantee against a bug. It lists the checks that exist.

## Controls

### 1. Credentials never reach disk or logs

| Control | Test |
|---|---|
| The optimizer, recorder and labelling code receive a copy of the headers that holds only the user agent, session id, encoding, content type and API version | `TestWithoutCredentialsKeepsOnlyWhatLaterCodeNeeds` |
| Every log line and error goes through `redact`: by field name (`key`, `token`, `secret`, `password`, `cookie`, `signature`, and so on), by well-known shape (`sk-`, JWTs, `Bearer`, AWS access key ids, GitHub, Google, Slack, Stripe and npm keys, PEM private key blocks) and by `name=value` | `redact.TestLeakThroughEveryPath`, `TestOtherCredentialShapesAreRedactedWithoutAName`, `TestOrdinaryTextSurvivesTheNewShapes` |
| The error text sent back to a client is redacted before it is cut to length | `TestShortErrRedactsBeforeTruncating` |
| Planted keys and prompt text are looked for in the logs, every response and every byte of every file in the data folder after every request path | `gateway.TestNoCredentialOrPromptReachesLogsResponsesOrDisk` (CI gate) |
| The same for prompt text, repo names, ticket titles and file paths through the real command-line and hook flows | `privacy.TestNoContentReachesTheDatabaseFilesOrAnythingShared` (CI gate) |
| The database schema holds metadata only: a new table or column fails a test until it is reviewed | `store.TestSchemaIsMetadataOnly` (CI gate) |
| A request that is not API-key traffic is refused with a plain explanation before anything is forwarded | `TestNonAPIKeyAuthIsRejected` |

### 2. The gateway only talks to local clients, and only forwards to the configured upstream

| Control | Test |
|---|---|
| Binds to loopback only: validated in the command, in `ListenAndServe` and on the real listener | `TestListenAddressMustBeLoopback`, `TestGatewayRefusesNonLoopbackListen` |
| DNS rebinding: the `Host` header must be `localhost`, `127.0.0.1` or `::1`; look-alike, userinfo, numeric and IPv4-mapped hosts are refused | `TestGuards`, `TestHostHeaderMatrix` |
| Cross-origin use: any `Origin` header is refused and no CORS header is ever sent | `TestGuards` |
| Not an open proxy: the upstream host is fixed from config and overwritten on every request; upstream URLs may not carry credentials, a query or a fragment | `TestNewRefusesUnsafeUpstreams` |
| Resource limits: a whole-request read timeout (5 minutes), a header timeout, an idle timeout, a 256 MiB cap per body, and a 1 GiB budget for bodies held in memory at once (a retriable 503 above it; a single request is never refused for size). There is deliberately no write timeout and no upstream response timeout: either could cut a valid long stream, and a client that disconnects already cancels the upstream call | `TestAStalledRequestIsCutOffButALongStreamIsNot`, `TestConcurrentLargeBodiesAreCappedAndRetriable`, `TestStreamIsNotBuffered` |
| Fail open: any optimizer error, panic or timeout forwards the original request | `optimize` fail-open tests |
| No outbound connection except the configured upstream and the two opt-in contribute calls | `egress.TestOnlyApprovedCodeCanReachTheNetwork`, `TestNoUnexpectedNetworkDependencies`; Linux `strace` job `TestCommandsMakeNoOutboundConnections` |

### 3. Files on disk

| Control | Test |
|---|---|
| Data folder 0700, database and write-ahead files 0600, recovery folder 0700 and files 0600, manifest 0600; a loose existing folder is tightened when the store opens; `trove doctor` reports loose modes | `TestFreshInstallPermissionsAndWAL`, `TestOpenTightensLoosePermissionsButDoctorPathDoesNot` (skipped on Windows) |
| Edits to agent files are marked, backed up, and undone byte for byte; an unclosed marker leaves the file alone; a backup is deleted once its undo succeeds | `TestUndoWithMissingCloseMarkerLeavesTheFileAlone`, `TestManifestIsPrivateAndBackupsDoNotOutliveAnUndo` |
| A symlinked config file (a dotfiles repository) is written through, so the link stays | `TestWritingThroughASymlinkKeepsTheLink` (runs where links can be created) |
| Retention: every table that holds history has an age rule, and the purge no longer needs the gateway | `TestEveryTableHasARetentionDecision`, `TestPurgeDeletesOldRowsFromEveryAgedTable` |
| The recovery store: opt-in, size-capped, auto-expiring, 0600, IDs are checked against the stored bytes | `recovery` tests |

### 4. Hooks, MCP and the plugin

| Control | Test |
|---|---|
| Hooks fail open: any error, panic or timeout exits 0 with no output, and the reason goes to a redacted log | hooks fail-open tests |
| Hook input is capped at 1 MiB; only known fields are decoded; session and prompt ids are hashed before they are used in a path; a file named in the input is only read for its size and hash, never stored | `TestNothingSensitiveIsStored` |
| Path tricks, NULs and very long ids in a hook's input never write outside the data folder and no path text reaches disk | `TestHostileSessionIDsAndPathsStayInsideTheDataFolder` |
| Hook commands written into an agent file are single-quoted for POSIX `sh`, and Trove refuses to write a command that depends on `PATH` | `TestShellQuoteSurvivesARealShell`, `TestNoBareTroveWhenPathUnknown` |
| `trove_run` runs nothing until the user approves that exact version of `.trove/commands.toml` with `trove mcp trust` (a real terminal, every command shown, no `--yes`); an edit withdraws approval | `TestRunRefusesUnapprovedCommandsFile`, `TestApprovalRunsAndEditWithdrawsIt`, `TestMCPTrustRefusesWithoutATerminal` |
| `trove_run` runs the listed program directly, never through a shell, with credential-looking variables removed (a name list, not an allowlist, see below) | `TestFilteredEnvDropsCredentialsAndKeepsToolchain` |
| `trove_read` and `trove_search` refuse files that usually hold secrets (by name, through a symlink too) and refuse paths that leave the project | `TestSensitive`, `TestReadRefusesSecretFiles`, `root` tests |

### 5. Autopilot, trials and the guardrails

Autopilot changes what the model sees, so it has its own controls.

| Control | Test |
|---|---|
| Autopilot only ever promotes optimizers its policy names, only from evidence, and only at a session boundary; sessions already running keep their settings | `TestStateIsPinnedForTheSession`, `TestAutopilotStatesReachTheSnapshot` |
| Widening needs consent: policy lossless-v3 needs its own yes; a user who agreed to v2 keeps exactly v2 until they agree again; setup's `--yes` never grants trials; the plugin command never turns trials on | `TestMovingFromV2ToV3NeedsANewYesAndKeepsWhatWasAlreadyKept`, `TestAV2UserIsNeverGivenTheNewCandidates`, `TestTrialsNeedTheirOwnExplicitYes` |
| The consent text is generated from the policy, so it cannot list fewer changes than the policy makes | `TestTrialsConsentNamesEveryCandidateAndQuotesThePolicysNumbers` |
| A user's own setting in `config.toml` always wins over Autopilot | `TestAdvanceLeavesAnOptimizerTheUserSetAlone` |
| Each trial candidate has a guardrail that sets it back to shadow by itself: repeat read (`hook_reread`), quiet commands (`hook_rerun`), trimmer (`hook_fetch_back`, stricter), read limit (`hook_extra_read`), `trove_run` hint (`hook_unused_advice`). They use the hook's own counts, labelled measured | `TestHookGuardrailsForRewritingOptimizers`, `TestReadLimitGuardrailFiresAtItsOwnLimit`, `TestTrimmerGuardrailIsStricterThanTheSharedRate`, `TestUnusedAdviceGuardrail` |
| A guardrail that cannot see what it needs does not fire: the hint guardrail ignores Codex sessions and only runs when the counter hook is installed; `trove hooks status` says when it is missing | `TestUnusedAdviceGuardrail`, `TestAdoptionIsObservableOnlyWhenTheRunHookIsInstalled`, `TestHooksStatusSaysWhenTheRunCounterIsMissing` |
| One assigner per optimizer: the hooks assign every trial arm (a hash of the session key), the gateway never does, so a session cannot get two arms | `TestHookAssignedTrialsMatchTheV3List` |
| Every optimizer's cost in added text is declared as numbers and charged (rule 15); the trimmer's 351 tokens of tool definitions is kept equal to the real schema | `TestEveryNameAPolicyListsIsChargeable`, `TestTheTrimmersDeclaredChargeMatchesTheToolDefinitions` |
| Hook events hold a hashed session key, a kind and two numbers; unknown kinds are dropped and the table refuses them | `TestNewKindsSurviveTheSpoolIntoTheDatabase`, `TestHookGuardrailKindsMigrationKeepsExistingRows` |
| A trial reads the agent's local history only while a hook trial runs, only with `[history] enabled`, with a bounded line reader, counts and hashes only, within a 700 ms budget | `trialscan` tests, `privacy` canary |

All thresholds are **placeholders** until the benchmark runbook sets them.
That is a statement about accuracy, not about security, but it is repeated here so nobody reads a threshold as proven.

### 6. Supply chain and releases

| Control | Where |
|---|---|
| Six direct dependencies, all pure Go, checked against `go.sum`; `go mod verify` runs in CI | `ci.yml` job `vulncheck` |
| Known vulnerabilities: `govulncheck` pinned to v1.9.0, run in CI | `ci.yml` job `vulncheck` |
| Static analysis: `gosec` through `golangci-lint`, and CodeQL (security-extended) on every push, pull request and weekly | `.golangci.yml`, `codeql.yml` |
| Actions pinned to commit SHAs and kept current by Dependabot (Go modules, Actions, pip) | `.github/dependabot.yml` |
| Releases: static binaries, keyless cosign signature over `checksums.txt`, one SPDX SBOM per archive, deterministic build flags | `.goreleaser.yaml`, `release.yml` |
| SLSA build provenance: each archive and `checksums.txt` is attested by the release workflow. Verify with `gh attestation verify <file> --repo usetrove/trove` | `release.yml` (`actions/attest-build-provenance`, pinned) |
| Secret scanning: a tracked file holding a credential-shaped string fails CI. Deliberate fakes carry `secretscan:allow` | `scripts/secretscan`, `ci.yml` job `secretscan` |
| Reproducible build check: the same commit built from two directories must give identical bytes | `scripts/rebuild-check.sh`, `ci.yml` job `reproducible` |
| `install.sh` verifies the archive's SHA-256 and, when cosign is installed, the signature (refuses on a bad one; `TROVE_REQUIRE_SIGNATURE=1` refuses unverified installs) | `install_test.sh` (fake cosign) |

## Known gaps, stated plainly

| Gap | Why it is open | Mitigation today |
|---|---|---|
| Recovery store holds raw output for up to 7 days (F-9) | Redacting it would break "the fetched output equals what the tool printed" | Opt-in; 0600; size cap; `[recovery] ttl_days`; `trove data delete` |
| Port squatting on a shared machine (F-13, R1) | Inherent to a fixed local port | Change `gateway.port`; single-user machines are unaffected |
| `trove_run` environment filter is a name list (F-28) | An allowlist breaks toolchains that cannot be listed in advance | The list covers the common credential names; `trove_run` runs only approved commands |
| npm wrapper checks the checksum only (F-15) | No sigstore verifier is bundled | `install.sh` verifies; verify by hand as in `SECURITY.md` |
| Reproducible builds: checked on one platform pair only (F-14) | `rebuild-check.sh` ran once here (Windows host, linux/amd64 target, identical bytes) and runs in CI on Linux; the full goreleaser archives are not compared | The CI job; deterministic flags and recorded Go version |
| SLSA provenance and secret scanning were added but have not run in CI | The attestation needs a tagged release on GitHub; `secretscan` passes locally and was checked against a planted key | The first release and the first CI run show whether they work |
| Windows mode bits and ACLs (R7, F-25) | Not a supported platform yet | None |
| Nothing from this update has run in CI | The review machine is Windows; local `golangci-lint` cannot type-check this Go version, and the `strace` test needs Linux | The local suite passes; CI is the authority |

## When something looks wrong

- `trove doctor`: permissions, schema, hooks, MCP and the capability matrix.
- `trove optimize off`: stops every optimizer at once. `trove autopilot off`: forgets Autopilot's states.
- `trove undo`: restores every agent file Trove edited. `trove uninstall --purge-data`: removes Trove and what it created.
- Report a vulnerability: see [../SECURITY.md](../SECURITY.md).
