# Trove for OpenCode

A plugin that runs Trove's optimizer engine inside [OpenCode](https://opencode.ai). The plugin
(`plugins/opencode`, TypeScript) holds no optimization logic: it passes tool calls, tool output and request bodies
to `trove engine serve` over the stdin/stdout protocol in [adapters.md](adapters.md) and applies the answer. Every
optimization is opt-in and starts in shadow mode, like everywhere else in Trove.

It never reads, stores or forwards a credential: the protocol has no field for one, and a test scans the plugin's
source for header, token, key and auth-hook access. It makes no network calls of its own.

Facts about OpenCode and the providers below were checked on **2026-11-05** (sources at the end of each section).
Re-check them before a release.

## Install

You need the `trove` program (Homebrew, npm or `install.sh`; see [getting-started.md](getting-started.md)) and a copy
of `plugins/opencode`. The package is not published to npm yet, so install it from a checkout.

- **OpenCode 2.x** (`@opencode/cli`): list the folder under `plugins` in `opencode.json`, as an absolute path or
  `file://` URL. The V2 docs say a configured local path must be a directory.
- **OpenCode 1.x** (`opencode-ai`): put the folder where OpenCode loads local plugins (`.opencode/plugins/` in a
  project, `~/.config/opencode/plugins/` for everything) or list it under `plugin`. How a *folder* plugin is
  loaded in 1.x is not verified (see [opencode-verification.md](opencode-verification.md)).

One package serves both: its default export has `server` (1.x) and `setup` (2.x). OpenCode ships both plugin APIs
side by side today and V1 plugins do not run in V2 (V2 migration guide, `anomalyco/opencode`, read 2026-11-05).

Plugin options (in `opencode.json`): `binary` (path of `trove`; default `trove` on PATH), `timeoutMs` (tool-level
calls, default 1000), `requestTimeoutMs` (default 2000), `debug` (fixed diagnostics to stderr, never message
contents). If `trove` is missing or speaks another protocol version, the plugin does nothing and OpenCode is
unaffected.

Then turn things on in `~/.trove/config.toml`, as for any adapter ([optimize.md](optimize.md)). Nothing is on by
default. A session keeps the settings it started with.

```toml
[optimizers]
noise_normalizer    = "shadow"   # then "on" once the report convinces you
hook_quiet_commands = "shadow"
hook_read_limit     = "shadow"
hook_repeat_read    = "shadow"

[optimize]
custom_agents = ["other"]        # only for volatile_prefix_mover and cache_breakpoints
```

`other` is the agent label a harness plugin gets, the same one the gateway gives a client it does not recognise
(only `sdk` and `other` can be marked custom). `trove report` shows what the plugin did under **Harness plugins**.

## What works where

The plugin can only do what OpenCode lets a plugin do. The columns are measured against the hook shapes in
`@opencode-ai/plugin` 1.19.5 (tag `v1.19.5`) and `@opencode/plugin` 2.2.5 (commit `e23d54ca`).

| | OpenCode 1.x | OpenCode 2.x |
|---|---|---|
| Rewrite a tool's arguments (`hook_read_limit`, `hook_quiet_commands`) | yes, `tool.execute.before` | yes, tool `execute.before` |
| Rewrite a tool's output text (`noise_normalizer`; `output_trimmer` once recovery is set up) | yes, `tool.execute.after` | yes, tool `execute.after` |
| Refuse a tool call (`hook_repeat_read`) | no documented way: **counted in shadow only** | no documented way: **counted in shadow only** |
| Rewrite the request body (`duplicate_result_collapser`, `volatile_prefix_mover`, `cache_breakpoints`, `noise_normalizer`) | no hook for the serialized body: **not available** | yes, `http.request`, **Anthropic Messages bodies only**; only the body is read, never the headers |
| Tell an API key from an OAuth login | partly (see below) | yes, the connection's method, without its secret |

Request-level transforms are Anthropic-only because the engine's request codec is ([adapters.md](adapters.md), known
gaps). OpenAI-format and Copilot traffic gets the tool-level transforms only.

Sources: `opencode.ai/docs/plugins/` (V1), `services/www/src/docs/content/build/plugins/*.mdx` and
`packages/plugin/src/promise/*.ts` on `dev` (V2), `packages/plugin/src/index.ts` and `tool/*.ts` at `v1.19.5`.

## Logins

The plugin tells the engine one of three logins, worked out from fields OpenCode shows for every provider:

| Login | How it is recognised | What Trove may do |
|---|---|---|
| `api_key` | V1: the provider's key comes from the environment or the config file. V2: the connection is an environment key or a stored key. | Change content. |
| `copilot` | the provider id is `github-copilot` or `github-copilot-enterprise` | Change content (tool level; request level is Anthropic-only). |
| `subscription_or_unknown` | everything else | **Watch only**: optimizers count what they would do and change nothing; request bodies are never touched. |

V1 cannot tell a stored API key from a stored OAuth login (both look like `source: "api"`), so in 1.x a key saved
with `opencode auth login` is treated as unclassified and stays shadow-only; use an environment variable or the
config file for a key if you want 1.x to change content. 2.x has no such limit.

### What the providers allow (read 2026-11-05)

| Login | Status | Evidence |
|---|---|---|
| GitHub Copilot in OpenCode | **Allowed** | GitHub changelog, 2026-01-16, "GitHub Copilot now supports OpenCode": "GitHub is officially supporting using your Copilot Pro, Pro+, Business, or Enterprise subscription with OpenCode." |
| ChatGPT Plus/Pro in OpenCode | **Not enabled: unconfirmed in a primary source** | OpenCode's provider docs describe the login, and OpenAI's developer docs describe a "Sign in with ChatGPT" program for open-source apps, but OpenAI's help article (HTTP 403) and its Terms of Use / Services Agreement could not be read, the program is a preview with request-shape restrictions (`store: false`, `stream: true`, some fields rejected), and Business eligibility is unclear. |
| Claude Pro/Max in OpenCode | **Not allowed** | Anthropic's legal-and-compliance page: OAuth is for the native applications, and third parties may not "route requests through Free, Pro, Max, or Ultra plan credentials on behalf of their users". OpenCode's own docs say the same. Trove never touches these (rule 10). |
| A plugin that changes request content | **Not addressed by any of the texts reviewed** | None of the three providers' terms, as read, says whether a third-party plugin may modify prompts or tool results while the user signs in normally. This document does not claim it is permitted. |

So: ChatGPT logins are held (shadow-only) until OpenAI's own terms have been read and confirm the use; Copilot is
enabled because GitHub supports OpenCode explicitly, with the caveat in the last row. Decision recorded in
[compliance.md](compliance.md).

## Measurement

The engine records, for each plugin session: the optimizer decisions it made (what changed, what would have
changed), and the usage and tool errors OpenCode reports, labelled with the harness and the login. Rows hold
hashed session keys, counts, sizes, model names and short fixed labels, never prompt text, paths, tool output or
credentials ([PRIVACY.md](../PRIVACY.md), table `adapter_events`).

- Which sessions run an optimizer for real is decided by the same A/B assignment as on the gateway
  (`[experiment]`, on by default): over each block of four sessions, two run it and two keep it in shadow, so its
  effect on actual cost and outcomes can be compared. Guardrail rollbacks lower an optimizer at once.
- `trove report` shows counts (measured), tokens kept out of the context (estimated from sizes, with the
  optimizer's own overhead beside it), and the token counts and cost *OpenCode* reported. A cost total is shown only
  for an API-key login and only when every call reported one; a Copilot login reports 0, which would read as free.
- Each optimizer declares its token overhead ([adapters.md](adapters.md)); the plugin itself registers no tools and
  injects no system text. The only text it adds is the engine's own short note (for example "this Read was limited
  to the first 400 lines"), which is counted.

## Overhead of the plugin-to-engine bridge

Measured with `node plugins/opencode/bench/bridge.bench.ts` on 2026-11-05: Windows 11 x64, Node 22.18 (and the same
file under Bun 1.3.14), a locally built `trove`, 300 calls per case after warm-up, wall-clock round trips as the
plugin sees them, milliseconds. "direct" is the same JSON lines written to a second `trove engine serve` with no
plugin code at all; the difference is what the plugin's own code costs. One machine's numbers, not a guarantee.

| Call | direct p50 | plugin p50 | plugin p95 | plugin code adds (p50) |
|---|---|---|---|---|
| `tool_input` (a Bash command) | 0.84 | 1.15 | 1.88 | 0.31 |
| `tool_output`, 1 KB | 0.82 | 0.98 | 1.69 | 0.17 |
| `tool_output`, 100 KB | 2.97 | 3.06 | 5.22 | 0.09 |
| `request`, 190 KB | 4.57 | 4.79 | 8.34 | 0.22 |

Under Bun the p50s were 0.83, 0.72, 2.95 and 2.93 ms. Cold start (spawn, open the database, handshake) was about
0.41 s on this machine. It happens once per OpenCode process, and the plugin starts the engine when it loads so it
is normally done before the first tool call. A call that arrives while the engine is still starting waits for it
(at most the 5 s start-up limit, once); if the engine cannot start, calls go through unchanged.

The bar was a bridge cost under 5 ms at p50 for tool-level calls and small requests (the gateway's bar). All cases
meet it; the 190 KB request is close to it (4.79 ms) and its time is dominated by the engine, not the bridge. A
call that takes longer than its timeout is abandoned and the original is used.

## Tests and parity

- `go test ./internal/gateway -run PluginParity`: for every optimizer the plugin adapter reaches, the plugin
  protocol must return what the gateway forwards (request level, byte for byte; tool output) and what the hooks
  adapter returns (tool output and tool input). A new optimizer fails here until it has a fixture.
- `cd plugins/opencode && npm test` (Node 22.18 or later): replays the same inputs through the plugin and the
  **real** `trove engine serve` and requires identical output, which shows the plugin's glue adds no logic. It also
  covers the bridge failing open (missing program, crash, slow or garbage replies, wrong protocol), the login gate,
  the V1 and V2 glue against the real engine and its database, the V2 request replacement (headers unchanged, new
  body on the wire), and the source scan for credential access and network use. Needs Go to build `trove`
  (or set `TROVE_BIN`).
- `npm run typecheck`: strict TypeScript.

## Known gaps

- Not run on a real OpenCode yet. Everything in [opencode-verification.md](opencode-verification.md) is unchecked
  until it is: how a local folder plugin loads in 1.x, whether a changed `output.args` is what the tool then runs
  with, whether `tool.execute.after` sees output before OpenCode's own truncation (2000 lines / 50 KB), whether
  assigning a new `Request` to `event.request` is honoured in 2.x (the docs only show header changes), the V2
  built-in tools' argument names, whether a provider id is the integration id `connection.active` expects, and the
  envelope of `ctx.event.subscribe` payloads.
- Refusing a tool call (`hook_repeat_read`) is counted as `would_block` only, in both APIs, until a way to refuse
  is verified.
- 1.x has no request-level transforms; non-Anthropic request bodies are never transformed.
- 1.x treats a key stored with `opencode auth login` as unclassified (shadow-only).
- ChatGPT logins are held; see above.
- The plugin has not been published to npm (that is an outward-facing step for you to decide).
- `hook_large_output` advice needs `trove_run`, which needs the Trove MCP server in OpenCode's MCP config; the
  plugin does not register it, so the advice stays off.
- The `output_trimmer` needs the MCP server and recovery store; until `trove mcp install` has run (it registers
  with Claude Code and Codex, not OpenCode) it stays in shadow.

## Upstream contribution candidates

Better as changes to OpenCode than as plugin work-arounds. For you to decide; none is started. OpenCode's
CONTRIBUTING (`dev`, read 2026-11-05) welcomes LLM-performance fixes and wants an issue and design approval before
new features.

1. **Read de-duplication in core.** A repeat `read` of an unchanged file is sent to the model again; core has
   truncation (2000 lines / 50 KB) and pruning but no read de-duplication (checked in `tool/read.ts`). This is the
   largest general win and fits "LLM performance improvements".
2. **A request hook that does not expose credentials.** V2's `http.request` hands the whole `Request`, including the
   authorization header, to every plugin. A body-only variant would let plugins like this one work without ever
   being able to read a credential.
3. **A non-secret auth method in 1.x hook inputs** (2.x already has `key | oauth | external`). Lets plugins stay
   out of credentials and removes the 1.x limitation above.
4. **Remove `key` from the `Provider` object passed to hooks**, if it is populated at runtime (unverified).
5. **Documented ordering of `tool.execute.after` against built-in truncation**, and access to the untruncated
   output, so output trimming can be lossless and recoverable.
6. **Stable, documented semantics for `experimental.chat.messages.transform`** (does an edit persist?), which would
   give 1.x a request-level path.
7. **Lossless noise clean-up of tool output** (ANSI colour codes, progress-bar redraws, repeated lines) next to the
   existing truncation.
8. **A way for a plugin to refuse a tool call** with a message to the model, which `hook_repeat_read` needs.
