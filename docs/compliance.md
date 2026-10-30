# Compliance checklist

**Date checked: 2026-10-30.** Not legal advice, and no legal conclusions are drawn here. For each source this
file says what the page says (short quotes), what Trove does, and whether Trove's behavior *contradicts any text
I could read*. Whether it satisfies the terms is a question for a lawyer; those questions are collected at the
end. Anything I could not read is marked **NOT VERIFIED** and nothing is asserted about it.

Re-run this check before each release (CLAUDE.md rule 8): terms and docs change, and one source below already
shows a future effective date.

## What Trove does that these sources could apply to

1. Sets `env.ANTHROPIC_BASE_URL` in `~/.claude/settings.json` (Claude Code) and adds a `trove` model provider to
   `~/.codex/config.toml` (Codex CLI), both with backups and an undo, so the agent talks to a gateway on
   `127.0.0.1`. No credential is written by Trove.
2. The gateway forwards the user's own requests to the provider and **only accepts API-key requests**. Anything
   that looks like subscription or OAuth traffic gets HTTP 401 and is not forwarded. Trove never logs, stores
   or echoes a credential, and never sets or changes client-identity headers: request headers are forwarded as
   received, except the hop-by-hop headers (such as `Connection`) that Go's reverse proxy removes by design
   (`gateway/proxy.go` `Rewrite` touches only scheme, host, path and body). An opt-in optimizer may change the
   request body; the response is returned unchanged.
3. Registers an MCP server (`trove`) and, optionally, command hooks with the agent, in the agents' documented
   locations.
4. Reads the agents' local session logs read-only, to compute token and cost metadata.
5. Runs only on the user's machine for the user who owns the key. It does not resell, share or pool access.

## Anthropic

| Source | Read? |
|---|---|
| [Claude Code: Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance) | Yes, in full |
| [Commercial Terms of Service](https://www.anthropic.com/legal/commercial-terms) | Yes. Page shows effective date **Jun 17, 2025** |
| [Usage Policy](https://www.anthropic.com/legal/aup) | Yes. Page shows effective date **Nov 12, 2026**, which is *after* the date checked; reason unknown, see questions |
| [Consumer Terms of Service](https://www.anthropic.com/legal/consumer-terms) | Linked from the page above; **NOT READ** |
| [Connect Claude Code to an LLM gateway](https://code.claude.com/docs/en/llm-gateway-connect), [Authentication](https://code.claude.com/docs/en/authentication), [gateway protocol](https://code.claude.com/docs/en/llm-gateway-protocol) | Yes |
| [Hooks](https://code.claude.com/docs/en/hooks) | First 100,000 of 283,467 characters read (configuration, handlers, stdin, exit codes, security notes). The rest **NOT READ** |
| [MCP](https://code.claude.com/docs/en/mcp), [Permissions](https://code.claude.com/docs/en/permissions) | MCP: first 100,000 of 154,630 characters. Permissions: read via targeted search of the saved page |

### Claude Code and API keys, custom endpoints, subscription credentials

- *Says:* "OAuth authentication is intended exclusively for purchasers of Claude Free, Pro, Max, Team, and
  Enterprise subscription plans"; developers "should use API key authentication"; Anthropic "does not permit
  third-party developers ... to route requests through Free, Pro, or Max plan credentials on behalf of their
  users. Moreover, developers may not collect, store, or intermediate Claude.ai credentials or session tokens."
  The restriction "does not restrict how customers provision and manage their own API keys ... provided the
  resulting usage is billed to the key owner ... and is not resold or intermediated." (Legal and compliance,
  *Authentication and credential use*.)
- *Trove:* API-key requests only; subscription-shaped requests are refused before forwarding; nothing is
  collected, stored or logged; usage is billed to the key owner; nothing is resold. Tests:
  `TestNonAPIKeyAuthIsRejected`, `auth_test.go`, `TestNoCredentialOrPromptReachesLogsResponsesOrDisk`.
- *Contradiction found:* none in the text read.
- *Says:* `ANTHROPIC_BASE_URL` plus `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY` is the documented way to use a
  gateway, set "in the `env` block of a settings file"; "Don't put the credential in a project's
  `.claude/settings.json`." (LLM gateway connect.) Setting only the base URL leaves a claude.ai login as the
  credential, and "a claude.ai login ... adds an OAuth capability" to `anthropic-beta`. (Gateway protocol.)
- *Trove:* writes only the base URL, to the user-level file, never a credential. A request with only a bearer
  token, or with an OAuth `anthropic-beta`, is refused. This means a user who configured a bearer-token gateway
  with `ANTHROPIC_AUTH_TOKEN` will be refused by Trove (it supports `x-api-key` only). That is a deliberate
  limit, stated in the refusal message.
- *Documented side effects of a custom base URL, which Trove users will see:* Remote Control is "disabled while
  `ANTHROPIC_BASE_URL` points at a non-Anthropic host"; voice dictation is unavailable while a gateway credential
  is active; the fast-mode availability check goes directly to `api.anthropic.com` and "doesn't follow
  `ANTHROPIC_BASE_URL`". **Trove's setup output does not mention these** (open item).
- *Says (products embedding Claude Code):* "The Claude Code binary must not be modified", and customers "may not
  remove, disable, or restrict any authentication method built into it"; "may not pay for, resell, or
  intermediate Claude usage on their end users' behalf."
- *Trove:* does not modify or redistribute the binary and is not a product offering Claude Code to end users.
  Whether refusing subscription requests at the gateway counts as "restricting" an authentication method for
  that clause is a question for a lawyer (the clause is written about products that host Claude Code).
- *Not found:* any statement on whether third-party tools may read `~/.claude/projects` session logs.
  **NOT VERIFIED** either way. Trove reads them read-only and keeps only numbers (see PRIVACY.md).

### Commercial Terms (effective Jun 17, 2025 on the page)

- *Says:* the customer may not "resell the Services except as expressly approved by Anthropic" (D.4(a)); "Customer
  is responsible for all activity under its account" (D.5); "Anthropic may not train models on Customer Content
  from Services" (B). The page "does not address API key sharing specifically."
- *Trove:* local, single user, no resale, no key sharing; keys stay in the user's own agent configuration.
- *Contradiction found:* none in the text read. These terms cover logging and retention only through documents
  that were not part of the page read (DPA, service-specific terms): **NOT VERIFIED**.

### Usage Policy (page shows effective Nov 12, 2026)

- *Says:* prohibits "reselling or proxying Claude access by unauthorized means, including routing requests
  through consumer subscriptions or misrepresenting the client being used", bypassing "guardrails", and notes
  "Users are responsible for ensuring that agents they build or deploy" comply.
- *Trove:* forwards headers unchanged and never sets or alters a client identity; refuses subscription traffic;
  does not alter model safety behavior. Optimizers (opt-in) change request *content* to save tokens, not
  safeguards.
- *Contradiction found:* none in the text read.

### Hooks and MCP in Claude Code

- *Says:* hooks live in `~/.claude/settings.json`, `.claude/settings.json` or `.claude/settings.local.json`;
  command hooks run via "`sh -c` on macOS and Linux"; a timed-out `PreToolUse` hook "doesn't block the tool call";
  "use the permission system rather than a hook to enforce a hard allow or deny." MCP servers are added with
  `claude mcp add` (scopes `local`, `project`, `user`); project-scoped `.mcp.json` servers need user approval in
  interactive sessions but load "without asking" in `claude -p`, Agent SDK and cloud sessions; "Servers that
  fetch external content can expose you to prompt injection risk." The permissions page says `Read` rules are
  applied "best-effort" to built-in tools and shows MCP tools matched by name (`mcp__server__tool`).
- *Trove:* installs hooks only in those locations, always fail-open, never as a security gate; registers MCP with
  `claude mcp add` at user scope by default (never edits `~/.claude.json`); `--scope project` writes `.mcp.json`.
  Because MCP file reads are not documented as covered by `Read` deny rules, `trove_read`/`trove_search` refuse
  secret-bearing files themselves. The hook command string goes through a POSIX shell, so paths are single-quoted.
- *Contradiction found:* none in the text read. Note the `claude -p` caveat: a project `.mcp.json` that Trove
  wrote is loaded without a prompt in non-interactive runs, as is anything else in that file.

## OpenAI

| Source | Read? |
|---|---|
| [Terms of use](https://openai.com/policies/terms-of-use/) | **NOT VERIFIED.** The fetch returned HTTP 403 |
| [Service terms](https://openai.com/policies/service-terms/) | **NOT VERIFIED.** HTTP 403 |
| [Business terms](https://openai.com/policies/business-terms) | **NOT VERIFIED.** HTTP 403 |
| [Usage policies](https://openai.com/policies/usage-policies/) | **NOT VERIFIED.** HTTP 403 |

A web search suggested that OpenAI's terms contain clauses about sharing account credentials and reselling
access, but those came from search-result summaries, some of them of archived versions. I did not read the source
text, so **nothing is asserted** about OpenAI's terms. This is the largest gap in this checklist: a person needs to
read these four pages in a browser and add the result here.

## Codex CLI

| Source | Read? |
|---|---|
| [Authentication](https://developers.openai.com/codex/auth) (redirects to learn.chatgpt.com/docs/auth) | Yes |
| [Advanced configuration](https://developers.openai.com/codex/config-advanced) (redirects to learn.chatgpt.com/docs/config-file/config-advanced) | Yes |
| [Hooks](https://learn.chatgpt.com/docs/hooks) | Yes |
| MCP page (`learn.chatgpt.com/docs/mcp`) | **NOT VERIFIED.** HTTP 404 at the URL tried. MCP configuration for Codex was last verified 2026-10-29 for `internal/fixer/paths.go` (config file locations), not re-checked today |

- *Says (auth):* "Sign in with ChatGPT for subscription access" or "Sign in with an API key for usage-based
  access"; with an API key, usage "follows your API organization's retention and data-sharing settings";
  "Treat `~/.codex/auth.json` like a password"; "Set `requires_openai_auth = true` to use OpenAI
  authentication ... useful when you access OpenAI models through an LLM proxy server", in which case "Codex
  ignores `env_key`."
- *Says (config):* "A model provider defines how Codex connects to a model (base URL, wire API, authentication,
  and optional HTTP headers)"; custom providers "can't reuse the reserved built-in provider IDs: `openai`,
  `ollama`, and `lmstudio`"; examples set `base_url` and `env_key = "OPENAI_API_KEY"`; local state is under
  `CODEX_HOME` (default `~/.codex`) and "local session transcripts" are saved there (for example
  `history.jsonl`).
- *Trove:* adds a provider named `trove` with `base_url` on loopback and `env_key = "OPENAI_API_KEY"`, and does
  not set `requires_openai_auth`, so Codex uses the API key from the environment and not a ChatGPT login. If a user
  sets `requires_openai_auth = true` on that provider, Codex would send its ChatGPT-login token to the gateway;
  Trove rejects JWT-shaped bearer tokens as a heuristic, which is **not a documented format** (finding F-4).
  Trove never opens `~/.codex/auth.json` (it is on the fixer's refuse list).
- *Not found:* the sessions/rollout files Trove parses for history (`~/.codex/sessions`) are not described on the
  pages read (only `history.jsonl` is). Trove's parser is based on observed files; the Codex hooks page says the
  transcript "format isn't a stable interface for hooks and may change." **NOT VERIFIED** as documented.
- *Says (hooks):* hooks are in "`hooks.json`" or inline `[hooks]` in `config.toml`; "Non-managed hooks must be
  reviewed and trusted before they run"; "Project-local hooks load only when the project `.codex/` layer is
  trusted"; "Treat tool hooks as a useful guardrail, not a complete enforcement boundary."
- *Trove:* writes user-level `~/.codex/hooks.json` only (it refuses to edit project hooks). **Users must review
  and trust the new hooks inside Codex before they run;** Trove's docs should say so (open item).
- *Contradiction found:* none in the text read.

## Open items found while doing this

1. Trove's setup output should mention Anthropic's documented side effects of a custom base URL (Remote Control,
   voice, fast-mode check) and that Codex needs to trust new hooks. Documentation only; not done.
2. Read the four OpenAI policy pages by hand and fill in the OpenAI section.
3. Read the remaining parts of the Claude Code hooks and MCP pages, and the Consumer Terms, and the DPA.

## Questions for a lawyer

1. A subscription user who runs `trove setup` and starts Claude Code with no API key has their OAuth token sent
   to the local gateway before it is refused. Trove does not log, store or forward it. Does merely *receiving* it
   on `127.0.0.1` fall under "collect, store, or intermediate Claude.ai credentials or session tokens"?
   (First raised in [legal-and-credentials.md](legal-and-credentials.md). A mitigation was proposed there:
   refuse `trove setup` unless `ANTHROPIC_API_KEY` is set. Not implemented.)
2. Is a local gateway that refuses subscription traffic "restrict[ing] any authentication method built into"
   Claude Code, for the clause written about customers offering Claude Code in their products?
3. Do Anthropic's and OpenAI's terms allow a third-party tool to read an agent's local session logs to compute
   usage and cost metadata for the same user?
4. Does an opt-in optimizer that rewrites the content of a user's own API request before forwarding raise any
   issue under either provider's terms?
5. The Usage Policy page shows an effective date of Nov 12, 2026, later than the date checked. Which version
   applies today?
6. Trove names Claude Code and Codex CLI in its docs and commands. Is that within each company's trademark
   guidance? (Anthropic's page says the Claude Code name may be used accurately in plain text and not as part of
   a product name or logo.)
