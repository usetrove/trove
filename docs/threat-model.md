# Threat model

Written 2026-10-30 as part of the security and privacy review (findings: [security-findings.md](security-findings.md)).
Every mitigation below names the code and the test that holds it in place. If a row has no test, it says so.

## What Trove is, for this purpose

A local program that (a) runs an optional HTTP gateway on `127.0.0.1` which forwards the user's own
API requests to the provider they configured, (b) edits a few of the user's agent config files, with backups,
(c) runs an MCP server and hooks that agents start, and (d) keeps metadata in `~/.trove`. It has no server,
no account, no update check, and no telemetry unless the user opts in to the anonymous summary upload. A single user runs it on their own machine.

## Assets

| Asset | Where it is | Why it matters |
|---|---|---|
| **API keys in transit** | In memory in the gateway for the length of one request; in the agent's own config or environment (not Trove's) | They spend money and can read the user's account. Trove must never log, store or echo them (CLAUDE.md rule 2). |
| **Prompts, code, tool output** | In memory in the gateway; in the recovery store if the opt-in output trimmer or `trove_run` stores output | Private source code and secrets pasted into prompts. Rule 7: never in the database. |
| **The local database** | `~/.trove/trove.db` (+ `-wal`, `-shm`) | Usage metadata. Not secret in itself, but reveals what the user works on and when. |
| **The recovery store** | `~/.trove/recovery/` | The one place raw tool output is written. May hold secrets that appeared in command output. |
| **Users' config files** | `~/.claude/settings.json`, `~/.codex/config.toml`, `~/.codex/hooks.json`, project `.mcp.json` and `.claude/settings.local.json` | Trove edits them. A bad edit breaks the agent; a malicious edit runs code. Backups in `~/.trove/backups/` hold full copies, which can contain keys. |
| **The binary itself** | Installed by `install.sh` or Homebrew | It runs with the user's full privileges and is started by their agents. |
| **The user's agent sessions** | Claude Code / Codex CLI, reached through the gateway, hooks and MCP | Whatever steers the agent can use Trove's tools. |

## Trust assumptions

- The OS account is the security boundary. Anything that can already run code as the user can read their
  keys, edit their config and replace the binary; Trove does not try to defend against that.
- The configured provider (Anthropic, OpenAI) is trusted with the user's key; that is the point of the key.
- The user's own `~/.trove/config.toml` is trusted. It chooses the upstream URL, so whoever can edit it can
  redirect keys. It is therefore created 0600 and the upstream is validated on load and again in the gateway.
- A repository the user clones is **not** trusted.

## Attackers

### A1. Other local processes and other local users

*Can:* connect to `127.0.0.1:8787`, read files the OS lets them read, race Trove for a port.

| Threat | Mitigation | Evidence |
|---|---|---|
| Read the database, backups or recovery store | Data dir 0700, database and `-wal`/`-shm` 0600, recovery dir 0700 and files 0600, manifest 0600; an existing loose data dir is tightened when the store opens; `trove doctor` reports loose modes | `store.Open` / `tightenPermissions`; `TestFreshInstallPermissionsAndWAL`, `TestOpenTightensLoosePermissionsButDoctorPathDoesNot`, `recovery` `TestPermissions`, `TestManifestIsPrivateAndBackupsDoNotOutliveAnUndo` |
| Read a backup of the user's settings (may hold keys) | 0600 in a 0700 folder; each backup is deleted once its undo succeeds | `setup.UndoSelected`; same test |
| Bind the gateway port before Trove does and receive the agent's key | **Not fully mitigated.** `trove gateway start` fails if the port is taken, but an agent already pointed at `http://127.0.0.1:8787` will send its key to whoever answers. See *Residual risk R1*. | none |
| Use someone else's gateway to inject records | The gateway forwards whatever key the caller presents and never adds one, so a second local user can only spend their own key; they can add rows to the owner's usage history and see `/trove/health` counters | `TestNonAPIKeyAuthIsRejected`; accepted, see R2 |
| Make the gateway listen on the network | Bind address is validated three times (CLI, `ListenAndServe`, `Serve` on the real listener); config has only a port | `ValidateListenAddr`; `TestListenAddressMustBeLoopback`, `TestGatewayRefusesNonLoopbackListen` |
| Tamper with `changes.json` so `trove undo` writes elsewhere | Needs write access to the data dir, which already means the OS boundary is lost. Not defended. | none |

### A2. A malicious web page (reaching localhost from a browser)

*Can:* make the visitor's browser send requests to `127.0.0.1:8787`, possibly after rebinding a DNS name to it.

| Threat | Mitigation | Evidence |
|---|---|---|
| DNS rebinding (page's own hostname resolves to 127.0.0.1) | `Host` must be `localhost`, `127.0.0.1` or `::1`; anything else is 403 | `server.go` `loopbackHost`; `TestGuards` (`evil.example`, `127.0.0.1.evil.com`, `10.0.0.5`) |
| Cross-origin fetch or form POST | Any request with an `Origin` header is 403, on every route including health | `TestGuards` |
| Read the response cross-origin | No `Access-Control-*` headers are ever sent; preflights carry `Origin` and are refused | `TestGuards` |
| Use the gateway as an open proxy | The upstream host is fixed from config and overwritten on every request; the client controls only path and query. Upstream URLs may not carry credentials, a query or a fragment, and `gateway.New` re-validates them | `proxy.go` `Rewrite`; `TestNewRefusesUnsafeUpstreams`, config tests |
| A page makes the browser send a key-bearing request | The browser cannot attach the user's key; the key lives in the agent, not in the browser | by construction |

A plain `GET /trove/health` from a navigation (no `Origin`) is answered. It returns the version, start time
and two counters; a page cannot read it cross-origin.

### A3. Malicious repository content (prompt injection)

*Can:* put text in files, tool output, `.trove/commands.toml`, `.mcp.json` or `CLAUDE.md` that steers the
agent into calling Trove's tools or runs when a tool opens it.

| Threat | Mitigation | Evidence |
|---|---|---|
| Agent runs `trove_run` and the repo's `commands.toml` names an arbitrary program | `trove_run` runs nothing until the user has approved that exact version of the file with `trove mcp trust`, which needs a real terminal, shows every command and has no `--yes`. Approval is a hash in `~/.trove`, so an edit withdraws it | `mcp/trust.go`; `TestRunRefusesUnapprovedCommandsFile`, `TestApprovalRunsAndEditWithdrawsIt`, `TestMCPTrustRefusesWithoutATerminal` |
| Agent uses `trove_read` / `trove_search` to pull `.env`, keys or `.git/config` into context | Files that usually hold secrets are refused and skipped by name, including through a symlink with an innocent name | `root/sensitive.go`; `TestSensitive`, `TestReadRefusesSecretFiles`, `TestSearchSkipsSecretFiles` |
| Path traversal or symlink escape out of the project | Absolute paths, `..` and symlinks leading outside are refused; the opened file is re-checked against what was resolved | `root.Resolve`/`OpenRead`; existing `root_test.go`, `read_test.go`, `search_test.go` |
| `trove_run` command steals the agent's keys from its environment | Credential-looking variables, `SSH_AUTH_SOCK` and `DATABASE_URL` are removed. This is a **denylist**, so an oddly named secret can pass | `filteredEnv`; `TestFilteredEnvDropsCredentialsAndKeepsToolchain` |
| Hostile hook input (tool input, session ids, file paths) | 1 MB stdin cap; only known fields decoded; session and prompt ids hashed before use in paths; a file named in the input is only stat'd and hashed, never stored; hooks always exit 0 | `hooks/input.go`; `hooks_test.go` (`TestNothingSensitiveIsStored`, fail-open tests) |
| A hook command line that breaks out of its quoting | Binary path and data dir are single-quoted for POSIX `sh`, tested against a real shell; Trove refuses to write a command that depends on `PATH` | `cli/shellquote.go`; `TestShellQuoteSurvivesARealShell` |
| A repo's `.mcp.json` or settings registering something hostile | Not Trove's to stop: the agent asks the user before using project-scoped MCP servers and trusts hooks per its own rules (see [compliance.md](compliance.md)). Trove only writes its own entries, between markers, with a backup | n/a |
| Output of a hostile command filling the disk or the context | Captured output is capped at 8 MB; the recovery store is size-capped and expires | `capWriter`; `recovery` tests |

### A4. Compromised dependencies or release artifacts

| Threat | Mitigation | Evidence |
|---|---|---|
| A dependency phones home or exfiltrates | Six direct dependencies, all pure Go, pinned in `go.sum`. A test fails if shipped code outside a short allowlist (the gateway, a loopback health probe, the opt-in upload, and `internal/stress/loopback.go`, whose dialer refuses any non-loopback address; plus the QA-only `qa/loadtest`, same rule) can dial or build an HTTP client, or imports a network-capable package; another fails on telemetry libraries in `go.mod`; a Linux test runs the real binary under `strace` and fails on any non-loopback `connect`/`send` | `internal/egress` tests; CI job `privacy` |
| Known vulnerabilities | `govulncheck ./...` in CI (pinned to v1.9.0). It found 10 reachable standard-library issues on Go 1.27.0; `go.mod` now requires 1.27.3, and the scan is clean | CI job `vulncheck`; [finding F-12](security-findings.md) |
| A tampered GitHub Action or tool in the build | Actions are pinned to commit SHAs; `govulncheck` is pinned; test and build jobs use the Go version from `go.mod` | `.github/workflows/*.yml` |
| A tampered release archive | `checksums.txt` is signed with keyless cosign and every archive has an SPDX SBOM. `install.sh` checks the archive's SHA-256 against `checksums.txt` from the same release before running anything. **They do not verify the cosign signature**, so they do not protect against a compromised release, only against corruption or a bad mirror. See R3 | `.goreleaser.yaml`; `install_test.sh` |
| A non-reproducible build hides a swap | The release stamps the commit date, not the build time, sets `-buildvcs=false` and a fixed file time. Reproducibility has **not been demonstrated** by rebuilding and comparing | `.goreleaser.yaml` |
| An unexpected network-capable module linked in | `golang.org/x/oauth2` is linked into the binary through the MCP SDK. No Trove code calls it; the strace test would catch a connection. See R4 | `go list -deps ./cmd/trove` |

### A5. A tampered config or environment

*Can:* edit `~/.trove/config.toml`, set `TROVE_UPSTREAMS_*`, or set `ANTHROPIC_BASE_URL` for the agent.

The upstream can be pointed at any `https://` host (or `http://` on loopback) with no path, credentials, query
or fragment, and the user's keys then go there. That is the user's own setting, so it is validated for shape
but not restricted to Anthropic or OpenAI hosts, because custom enterprise endpoints are a supported use.

## What happens to credentials, end to end

1. The agent sends the key in `x-api-key` or `Authorization`.
2. `auth` classifies the request by header shape and compares values in constant time. It rejects anything
   that is not API-key traffic with HTTP 401 and a plain explanation, **before** forwarding, and never echoes
   the credential.
3. After that point, everything downstream (optimizer, recorder, session and agent labelling) gets a copy of
   the headers containing only `User-Agent`, session id, encoding, content type and API version.
4. The key is forwarded to the configured upstream only, in the original request.
5. Logs go through a redacting handler; the error text returned to the client is redacted before it is cut;
   panics are formatted through the same redaction.
6. `TestNoCredentialOrPromptReachesLogsResponsesOrDisk` plants keys and prompt text and scans logs, responses
   and every byte of every file in the data dir after success, stream, upstream error, upstream down,
   rejection and oversize-body paths. It was checked to fail when a key is written to a column.

## Residual risks (accepted or open)

- **R1. Port squatting.** The gateway is plain HTTP on a fixed loopback port. On a machine with other users,
  or if malware binds the port first, the agent's key goes to the squatter. Mitigations to consider: a random
  port written into the agent config, or a Unix socket. Open (finding F-13).
- **R2. Any local user can call the gateway.** They cannot spend the owner's key, but can add usage rows and see
  health counters. Accepted for a single-user tool.
- **R3. Release signature is not verified by the installers.** Open (finding F-15).
- **R4. A dormant `oauth2` dependency is linked in.** Open, low (finding F-16).
- **R5. A process with the user's privileges can do anything**, including editing `mcp_trust.json`. Out of scope;
  the TTY requirement on `trove mcp trust` only stops an agent from approving through its own shell
  *input*, not a process that edits the file directly.
- **R6. Name-based secret detection.** The file denylist and the environment filter work on names. A secret in
  an ordinary file or variable is not caught.
- **R7. Windows.** Mode bits are not meaningful and Trove sets no ACLs; the hook and MCP process-group
  handling is incomplete. Windows is not a supported platform yet.
- **R8. Memory.** A request body of up to 256 MiB is buffered per request with no concurrency cap (finding F-7).
