# Security and privacy review: findings

Review date: 2026-10-30. Scope: the whole repository at commit `86eab15`, reviewed against the rules in
CLAUDE.md. Companion documents: [threat-model.md](threat-model.md), [../PRIVACY.md](../PRIVACY.md),
[compliance.md](compliance.md).

## Result

| Severity | Found | Fixed in this review | Open |
|---|---|---|---|
| Critical | 0 | 0 | 0 |
| High | 3 | 3 | 0 |
| Medium | 12 | 6 | 6 |
| Low | 14 | 4 | 10 |

No critical or high finding is open. One finding first rated high (F-4) was re-rated low after checking the
provider documentation, for the reason given there.

## How this was done, and what it does not prove

Read: the gateway, auth, redaction, config, store, setup/undo, fixer, MCP server, hooks, logs parsers, CLI, CI
and release files, install scripts and the npm wrapper. Ran: the full Go test suite on Windows with Go 1.27.3,
`govulncheck`, and mutation checks on the two new CI gates (the leak test was made to fail by writing a key to a
database column; the egress test by adding an `http.Get`).

**Not done, and why it matters:**

- **Nothing has run in CI.** No push was made. The `privacy` job, the pinned actions and the strace test have
  never executed on GitHub's runners.
- **The strace egress test has not been run at all.** It needs Linux and `strace`; this machine is Windows
  (WSL has only the Docker distro and Docker was not running). Its output parser is unit-tested on canned
  `strace` lines and the driver compiles for Linux, but the driver's first real run is in CI and may need
  adjusting.
- **Permission-mode tests are skipped on Windows** (they are in `store`, `setup`, `mcp`), so file-mode fixes
  were exercised by code review and will first run on the Linux and macOS CI jobs.
- **Reproducible builds were configured, not demonstrated.**
- **`golangci-lint` could not be re-run after the Go 1.27.3 bump** (the prebuilt linter here cannot type-check
  it; CI builds the linter from source). It ran clean on my changes before the bump except two items I then fixed.
- **No real agent was used.** Claude Code and Codex CLI were not run against the changes; `docs/hooks-verification.md`
  and `docs/mcp-verification.md` list what to check on a real install.
- Several OpenAI sources could not be read (see compliance.md).
- One timing test (`TestEngineStaysWithinTheGatewayBudget`) failed once under the parallel suite and passed three
  times alone; I did not change that package (F-29).

## Findings

Status: **Fixed** (commit), **Open**. "Evidence" is where to look; "Fix" is what was done or what should be.

### High

**F-1. `trove_run` executed whatever a cloned repository's `.trove/commands.toml` listed.** *Fixed (`be72cbd`).*
- Evidence: `internal/mcp/run.go` loaded the allowlist from the project and ran it directly with the user's
  privileges whenever the agent asked. A repository plus a prompt-injected agent was code execution, with none
  of the agent's own permission prompts for Bash.
- Fix: the file counts only after the user approves that exact version with `trove mcp trust` (hash stored in
  `~/.trove/mcp_trust.json`, keyed by a hash of the project path). It needs a real terminal, shows every command
  and has no `--yes`. Any edit withdraws approval. Damaged trust file approves nothing.
- Tests: `TestRunRefusesUnapprovedCommandsFile`, `TestApprovalRunsAndEditWithdrawsIt`,
  `TestTrustIsPerProjectAndStoresNoPaths`, `TestDamagedTrustFileApprovesNothing`, `TestMCPTrustRefusesWithoutATerminal`,
  `TestMCPTrustApproveShowsCommandsThenRevoke`, `TestMCPTrustDeclineApprovesNothing`.

**F-2. `trove uninstall` was a stub.** *Fixed (`2d899e5`).*
- Evidence: `internal/cli/root.go` registered a command that printed "not implemented yet"; the brief and this
  review require full removal and restore.
- Fix: stops the gateway and removes its service, restores every edited agent file (byte for byte when
  unedited), removes the MCP server registered through the agent's own CLI, and with `--purge-data` deletes only
  the files Trove created. It never deletes data if a step failed, leaves any file it did not create (the data
  folder name may be shared with another tool on the user's machine), and deletes `trove.db` only if it
  has this program's migration history. Plan shown first; `--yes`, `--dry-run`, `--json`.
- Tests: `TestUninstallRestoresEveryAgentFileAndPurgesData`, `TestUninstallKeepsDataByDefault`,
  `TestUninstallWithoutYesOrTerminalChangesNothing`, `TestPurgeLeavesForeignFilesAndForeignDatabase`,
  `TestPurgeIsRefusedWhenAStepFails`, `TestUninstallJSONAndNothingToDo`.

**F-3. Hook and status-line commands were quoted only for spaces.** *Fixed (`7cdba26`).*
- Evidence: `internal/cli/hooks.go`, `setup.go`. A binary path or data dir containing `"`, `$`, a backtick, `;`
  or `&` would break out of the command Claude Code runs through `sh -c` (documented in compliance.md). If the
  executable path could not be resolved, the command fell back to a bare `trove`, which resolves via `PATH`.
- Fix: POSIX single-quoting (`cli/shellquote.go`), and Trove refuses to write a command that depends on `PATH`.
- Tests: `TestShellQuoteSurvivesARealShell` (the shell is the judge), `TestHookCommandsQuoteHostilePaths`,
  `TestStatusLineCommandQuotesDataDir`, `TestNoBareTroveWhenPathUnknown`.

### Medium

**F-5. Undo deleted the rest of a file when the closing marker was missing.** *Fixed (`63e3486`).*
`stripMarked`/`stripBetween` turned on skipping at the opening marker and never turned it off. Now an unclosed
block is an error and the file is left alone. Tests: `TestUndoWithMissingCloseMarkerLeavesTheFileAlone`,
`TestStripBetweenRefusesUnclosedBlock`.

**F-6. Permission gaps.** *Fixed (`63e3486`).* (a) `changes.json` was created 0644 by the generic atomic writer;
(b) an existing data folder or database with group/other access was never tightened (only `doctor` reported it);
(c) backups, which are full copies of the user's settings and can hold API keys, were never deleted. Now the
manifest is 0600, `store.Open` removes group/other access (not `OpenNoMigrate`, which `doctor` uses, so it still
sees the real state), each backup is deleted once its undo succeeds and kept if it did not, and atomic writes
`fsync`. Tests: `TestManifestIsPrivateAndBackupsDoNotOutliveAnUndo`, `TestBackupIsKeptWhenUndoCouldNotFinish`,
`TestOpenTightensLoosePermissionsButDoctorPathDoesNot` (skipped on Windows).

**F-10. `trove_read`/`trove_search` could return `.env`, key files and `.git/config`; `trove_run` passed on
more environment than it should.** *Fixed (`266fb21`), partly.* Claude Code documents `Read` deny rules as applying
best-effort to its built-in file tools and says nothing about files an MCP server reads, so Trove does not rely on
them. Secret-bearing files are now refused by name (including through a symlink named innocently) and skipped by
search; the environment denylist now also drops `SSH_AUTH_SOCK`, `DATABASE_URL`, cloud, cookie, `*_PAT` and
`*_DSN` names. Still name-based; see F-28. Tests: `TestSensitive`, `TestReadRefusesSecretFiles`,
`TestSearchSkipsSecretFiles`, `TestFilteredEnvDropsCredentialsAndKeepsToolchain`.

**F-12. The standard library of Go 1.27.0 had 10 vulnerabilities reachable from the gateway.** *Fixed (`de35d78`).*
`govulncheck` reported GO-2026-6809 (x509 distrust list), -6617, -6613, -6612, -6611, -6610, -6608, -6607, -6605,
-6603 (net/http, HTTP/2, mime, crypto/tls), fixed in 1.27.2 and 1.27.3. Released binaries are built with the
newest stable Go, so were likely unaffected; source builds and the CI jobs I pinned to `go.mod` would have been.
`go.mod` now requires 1.27.3; `govulncheck` reports none.

**F-14. Supply chain: floating CI actions and tools, non-reproducible release.** *Fixed (`4016898`), not demonstrated.*
Actions pinned to commit SHAs, `govulncheck` pinned to v1.9.0, test/privacy/build jobs use the `go.mod` Go
version, and the release stamps the commit date, uses `-buildvcs=false` and a fixed file time. The release job
keeps `go-version: stable` on purpose (for stdlib fixes), so exact reproduction needs the Go version recorded in
each binary. Not yet verified by rebuilding and comparing.

**F-17. Upstream URL validation and client-facing error redaction.** *Fixed (`266fb21`).* An upstream could carry
a username/password, query or fragment, and `gateway.New` trusted `config.Load` to have checked it. The upstream
transport error text sent to the client was cut to 120 characters but not redacted. Now both are checked in the
gateway, and redaction runs before truncation. Tests: `TestNewRefusesUnsafeUpstreams`, config table tests,
`TestShortErrRedactsBeforeTruncating`.

**F-7. No read/write timeouts or concurrency cap; request bodies up to 256 MiB are buffered per request.**
*Open.* `server.go` sets `ReadHeaderTimeout` and `IdleTimeout` only; `proxy.go` reads the body with `io.ReadAll`;
the upstream transport has no response-header timeout. A local client can hold memory. Not changed because
timeouts interact with long streams and slow providers and need a design; a semaphore on in-flight body bytes and
a `ResponseHeaderTimeout` are the likely fix.

**F-8. Retention covered four of fifteen tables.** *Closed 2026-11-06.* `Purge` now has an age rule for every table that
holds history (`internal/store/retention.go`), and each remaining table is listed there with the reason it is kept.
`TestEveryTableHasARetentionDecision` fails if a table has neither, and `TestPurgeDeletesOldRowsFromEveryAgedTable`
checks each rule. The purge runs at most once a day from the gateway, from the session-end hook and from `trove report`
(`PurgeIfDue`), so it no longer needs the gateway. `history_files` (a path hash and offsets) stays by design so purged
history is not read again; `trove data delete` removes it. Reverted fixes expire; fixes still in place are state and do not.

**F-9. The recovery store is global and holds raw, unredacted output.** *Open.* Files are named by the first 16 hex
characters of the content's SHA-256, any MCP client can fetch any ID, IDs are not scoped to a project, and output
(which may contain secrets) is stored as is for up to 7 days. It is the documented opt-in exception to rule 7.
Consider scoping IDs to a project and redacting known secret shapes before storing.

**F-11. Symlinked config files are replaced by regular files on write; fixer symlink behavior is untested.**
*Open.* `setup.writeAtomic` renames over the path, so a dotfiles-managed `settings.json` symlink is replaced.
Separately, `fixer.Env.Allowed` compares resolved paths, so a repository `CLAUDE.md` that is a symlink to another
file resolves equal to itself; only the credential-name checks then stop it. `TestAllowedRefusesSymlinkOutsideAllowedSet`
exists but I did not confirm it covers a link at an allowlisted name.

**F-13. Port squatting on a shared machine.** *Open.* The gateway is plain HTTP on a fixed loopback port. If
another local user or process binds `127.0.0.1:8787` first, an agent already pointed there sends its API key to
them. `trove gateway start` fails when the port is taken but the agent does not know. Inherent to this design;
options are a random port written into the agent config, or a Unix socket. Matters most on multi-user machines.

**F-15. Installers verify a same-origin checksum, not the signature.** *Open.* `install.sh` and the npm wrapper
compare the archive's SHA-256 with `checksums.txt` from the same release. The release has a cosign signature
over `checksums.txt`, but neither installer checks it, so a compromised release (which controls both files) would
pass. Rated medium because exploiting it needs a compromised release and the checksum does still catch corruption
and mirror tampering. Fix: verify the sigstore bundle (shell: `cosign verify-blob`, with a documented fallback;
npm: a sigstore verifier) before running anything.

### Low

Fixed: **F-18** the optimizer and record path received the full request headers including the API key; they now
get an allowlisted copy (`d059203`, `TestWithoutCredentialsKeepsOnlyWhatLaterCodeNeeds`). **F-19** `model`, `endpoint`
and `error_class` were stored without a length bound (`33e28f3`, `TestClientControlledFieldsAreCappedInTheRecord`).
**F-20** `TestSchemaIsMetadataOnly` covered 8 of 15 tables; it now lists all and fails on an unreviewed table
(`33e28f3`). **F-21** README said "foundation only" and SECURITY.md lacked pointers; corrected in this review's final commit.

Open:
- **F-4. Anthropic OAuth tokens in `x-api-key` are not recognised by prefix; OpenAI detection is JWT-shaped only.**
  First rated high; re-rated low. Anthropic's docs describe a claude.ai login as sending the token as a bearer
  with an OAuth `anthropic-beta` value, and Trove refuses both. Neither the Claude Code nor the gateway pages
  document a token prefix, and CLAUDE.md rule 8 forbids building on one from memory, so a prefix check was not added.
  OpenAI (updated 2026-11-06): the route is now an allow-list that accepts only `sk-` bearer keys and refuses every
  other credential type, including ones not seen before; the `sk-` prefix is itself undocumented, so the failure mode is
  a wrongly refused key, not an accepted login. Codex sends a ChatGPT token only if the user sets
  `requires_openai_auth = true`, which Trove's setup does not. Revisit if either provider documents formats.
- **F-16.** `golang.org/x/oauth2` is linked into the binary through the MCP SDK (`go list -deps ./cmd/trove`).
  Nothing in Trove calls it and the strace test would catch a connection; it is dormant network-capable code.
- **F-22.** `net.Listen("tcp", "localhost:N")` follows whatever `localhost` resolves to; the CLI always uses `127.0.0.1`.
- **F-23.** Path and content hashes stored by hooks and history are unsalted 16-hex SHA-256 prefixes; a low-entropy
  path can be confirmed by guessing. Not reversible in general.
- **F-24.** The launchd unit writes its log to `~/.trove/gateway.log` even if a different data folder was chosen.
- **F-25.** Windows: no ACLs set, mode checks skipped, process-group kill is a no-op, no Windows CI.
- **F-26.** Free-text redaction recognises `sk-`/`rk-`/`pk-` keys, JWTs, `Bearer`/`Basic` and `key=value`; other
  formats (AWS, GitHub, Google keys) are caught only next to a sensitive field name.
- **F-27.** Hooks installed before F-3 with a path containing spaces used double quotes; `trove hooks uninstall`
  matches the new prefix, so those entries need `trove undo` instead. Rare.
- **F-28.** `trove_run` filters the environment with a name denylist, not an allowlist. An allowlist would be safer
  but risks breaking toolchains I cannot test here.
- **F-29.** `TestEngineStaysWithinTheGatewayBudget` is load-sensitive (one failure in the parallel run, three passes alone).

## New gates in CI

| Gate | Test | What fails it |
|---|---|---|
| Key and prompt leakage | `gateway` `TestNoCredentialOrPromptReachesLogsResponsesOrDisk` | A planted key or prompt text in logs, any client response, or any byte of any data file |
| Metadata-only schema | `store` `TestSchemaIsMetadataOnly` | A new table or column that was not reviewed, or a content-looking column |
| No egress (static) | `egress` `TestOnlyApprovedCodeCanReachTheNetwork`, `TestNoUnexpectedNetworkDependencies` | Network client code outside three files, banned imports, telemetry modules |
| No egress (dynamic) | `egress` `TestCommandsMakeNoOutboundConnections`, `TestGatewayOnlyTalksToItsConfiguredUpstream` | Any non-loopback `connect`/`send` by the built binary (Linux, `strace`; first CI run pending) |
| Vulnerabilities | CI `vulncheck` | Reachable vulnerabilities (pinned scanner, newest stable Go) |

## What to verify next

1. Push a branch and confirm the `privacy` job passes on GitHub's runners; fix the strace driver if needed.
2. On a real macOS and Linux install: `trove setup`, `hooks install`, `mcp install`, then `trove uninstall --purge-data`
   and diff the home folder against a snapshot.
3. Read the four OpenAI policy pages and fill in compliance.md; have the open questions there reviewed.
4. Decide F-13 (port squatting), F-15 (signature verification) and F-7 (limits), the three open mediums that
   most change a user's exposure.
5. Rebuild a release twice and compare checksums to confirm F-14.
