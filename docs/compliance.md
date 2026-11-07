# Compliance checklist

**Date checked: 2026-10-30; see "Update 2026-11-04" at the end.** Not legal advice, and no legal conclusions are drawn here. For each source this
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
| [Terms of use](https://openai.com/policies/terms-of-use/) | **Read 2026-10-31**, from text the maintainer copied out of a browser. The page header read "Effective: January 1, 2026 (Previous version)", so confirm it is the current version |
| [Service terms](https://openai.com/policies/service-terms/) | **Read 2026-10-31** (copied from a browser). "Updated: October 16, 2026" |
| [Business terms](https://openai.com/policies/business-terms) | **Read 2026-10-31** (copied from a browser). It is titled "OpenAI Services Agreement", "Updated: December 1, 2025", effective January 1, 2026 |
| [Usage policies](https://openai.com/policies/usage-policies/) | **Read 2026-10-31** (copied from a browser). "Effective: November 20, 2026", **after the date read**; the changelog says it was announced 2026-10-21 |

Not read: the Data Processing Addendum, the Service Credit Terms, the Sharing and Publication Policy, the Brand
Guidelines, and the API documentation the Service terms point to. **Nothing is asserted about those.**

What the text says (quoted from the copied pages; section numbers are theirs):

- *Which terms govern what.* The Terms of Use say "Our Business Terms govern use of ChatGPT Enterprise, our
  APIs, and our other services for businesses and developers." So API-key traffic, which is all Trove forwards,
  falls under the Services Agreement (3.1, 3.3 and 10 below), and the consumer Terms of Use are about ChatGPT.
- *Account and key sharing (Services Agreement 3.1, 3.3(g)).* "Customer may not resell or lease access to its
  Account or any End User Account." Customers may not "buy, sell, or transfer API keys from, to, or with a third
  party." Trove runs locally for the person who owns the key, keeps the key in memory only, and does not transfer
  it. The consumer Terms of Use similarly say "You may not share your account credentials"; Trove never touches
  ChatGPT credentials and rejects JWT-shaped bearer tokens (heuristic, F-4 above).
- *Limits and extraction (Services Agreement 3.3(f), (h), (i)).* Customers may not "circumvent any rate limits or
  restrictions", "violate or circumvent Usage Limits or otherwise configure the Services to avoid Usage Limits",
  or "extract data from the Services other than as permitted through the Services". Trove forwards the user's own
  requests and trims or reorders only the user's own input. **Rule for future work:** no optimizer may be
  designed to dodge a rate limit or a Usage Limit. The consumer Terms of Use also bar "Automatically or
  programmatically extract[ing] data or Output"; that is why gateway mode must stay API-key only (rule 3).
- *Content (Services Agreement 4.2).* OpenAI uses Customer Content only to provide the Services, comply with law,
  enforce policies and prevent abuse, and "will not use Customer Content to develop or improve the Services,
  unless Customer explicitly agrees". Nothing here concerns Trove, which sends nothing to OpenAI beyond the
  user's own requests.
- *Model-access intermediaries (Usage policies).* "If you operate a model-access intermediary platform or other
  service that enables third parties to directly access OpenAI models, you must obtain our prior review and
  approval. This requirement does not apply solely because an application integrates OpenAI models to power its
  features or functionality." **This is the clause closest to Trove.** Trove is not a hosted platform: it
  listens on loopback only, refuses other bind addresses, and serves the person who owns the key, so it does not
  "enable third parties" to reach OpenAI models. That is our reading, not OpenAI's, and the phrase "other service"
  is broad. It is in force from 2026-11-20. **Rule for future work:** the gateway must never listen on a
  non-loopback address, share one key between people, or be offered as a hosted service; any such feature needs
  OpenAI's review first (see lawyer question 7).
- *Training clause (Usage policies).* Models and outputs must not be used to generate, select or supply training
  material for models that are "alternatives to or substitutes for OpenAI models". The same section allows
  "research, software development, model evaluation and benchmarking, and product development". Trove stores
  metadata only (rule 7), never response text, and its benchmarks compare agent runs; neither builds a
  competing model.
- *API documentation (Service terms 1(a)).* "Customer will only ... use APIs in accordance with the applicable
  documentation at https://platform.openai.com/docs." Trove's OpenAI behavior (endpoints, streaming, usage
  fields) still has to be checked against that documentation under rule 8; this review did not do it.
- *Codex (Service terms 4).* "Output generated by code generation features of our Services, including OpenAI
  Codex, may be subject to third party licenses, including, without limitation, open source licenses." A note for
  users, not a constraint on Trove.
- *Publicity (Services Agreement 10).* "neither Party will: (i) include the other Party's name or logo on their
  websites, media, or marketing materials; or (ii) make any public statement about its relationship with the
  other Party". This binds OpenAI and each customer under that Agreement, not an open-source tool that merely
  names a provider. Still, Trove's launch posts should describe compatibility in plain text, use no OpenAI
  logo, and imply no partnership (lawyer question 6).
- *Contradiction found:* none in the text read. Nothing read forbids a local, single-user gateway on the user's
  own API key. The model-access-intermediary clause is the one place where the wording is broad enough that a
  person should confirm.

Caveat: the copied Terms of Use is labeled "(Previous version)" in its header and the Usage policies carry a
future effective date, so OpenAI's pages may change again soon. Re-read before each release.

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

## Second pass (2026-10-30, later the same day)

Re-read with a page-fetch tool that returns a summary of each page, so the quotes below are the tool's
extracts and **should be spot-checked in a browser before anyone relies on them legally.** Nothing here
changes the first pass's conclusion: **no contradiction found in the text read.**

| Source | Result |
|---|---|
| Claude Code, Legal and compliance | Re-read in full. Differences from the first pass: subscription plans now listed as "Free, Pro, Max, Ultra, Team, and Enterprise" and the restriction names "Free, Pro, Max, or Ultra plan credentials"; new sentence that advertised plan limits "assume ordinary, individual usage"; new statement that under the HIPAA configuration, Claude Code "routed through a third-party cloud provider or a gateway" is not covered; the name-and-logo paragraph is as before. |
| Claude Code, Connect to an LLM gateway | Re-read in full. Consistent with the first pass. New detail: "A gateway credential variable takes precedence over a saved claude.ai login", and `ANTHROPIC_API_KEY` needs a one-time approval in interactive sessions; a previously declined key is "ignored without asking again" (fix: `/config`, `Use custom API key`). Trove's walkthrough already tells users to say yes to that prompt. |
| Commercial Terms | Effective date still **Jun 17, 2025**. D.4(a), D.5 and B confirmed as quoted above. |
| Usage Policy | Page still shows **Nov 12, 2026** (after today). The fetch returned this wording, which differs from the first pass's quote, so treat the first pass's quotation marks there as a paraphrase: "Resell, proxy, or otherwise provide access to Claude through unauthorized means" ... "including services that route requests through consumer subscriptions or misrepresent the product or client being used"; and "Users are responsible for ensuring that agents they build or deploy ... comply with this Usage Policy." |
| Consumer Terms | **Now read.** Effective **Oct 8, 2025**. Section 3 prohibits accessing the Services "through automated or non-human means, whether through a bot, script, or otherwise" *except* "when you are accessing our Services via an Anthropic API Key or where we otherwise explicitly permit it"; Section 2: "You may not share your Account login information, Anthropic API key, or Account credentials with anyone else"; Section 3 also prohibits reselling. The page does not mention Claude Code. Trove is API-key only, local and single-user. Which terms govern a given user's usage (Consumer or Commercial) is a lawyer's question. |
| Claude Code, Hooks | Read characters 0 to about 200,000 of 284,258, plus a keyword scan of the rest (which includes the Security considerations section). Trove's hook output matches the documented form: `hookSpecificOutput.permissionDecision` (top-level `decision` is deprecated for `PreToolUse`), and `additionalContext`. New since the first pass: an exec form (`args`, no shell), `onFailure: "block"` (a timed-out hook otherwise "doesn't block the tool call"), Git Bash or PowerShell on Windows, and default timeouts of 600 s (30 s for `UserPromptSubmit`). **Workspace trust:** in interactive sessions Claude Code "holds back hooks from every settings file, including your own `~/.claude/settings.json`, until you accept the workspace trust dialog"; in `-p` and SDK sessions the folder is treated as trusted. So Trove's hooks will not run until the user has accepted trust in that folder (open item 1). |
| Claude Code, MCP | Read characters 0 to 100,000 of 154,700, plus a keyword scan of the rest. Scopes, storage (`~/.claude.json` for local and user, `.mcp.json` for project), the interactive approval prompt and the `claude -p` loads-without-asking behavior are as in the first pass. New: a warning above 10,000 tokens of MCP output, and a successful result over 50,000 characters is saved to a file by Claude Code (unless the tool declares `anthropic/maxResultSizeChars`); tools whose full name exceeds 128 characters are left out. |
| Codex: Authentication, Advanced configuration, Hooks | Re-read. Consistent with the first pass. `~/.codex/sessions` is still **not documented** (only `history.jsonl` is). |
| Codex: MCP page | Not re-checked. |
| OpenAI Terms of use, Service terms, Business terms, Usage policies | **Read on 2026-10-31** from text copied out of a browser; see the OpenAI section. (The fetch tool still gets HTTP 403.) |
| Data Processing Addendum | **NOT READ.** |

## Open items found while doing this

1. Trove's setup output should mention Anthropic's documented side effects of a custom base URL (Remote Control,
   voice, fast-mode check) and that Codex needs to trust new hooks; the second pass adds that Claude Code also
   withholds hooks until workspace trust is accepted. Documentation only; not done.
2. ~~Read the four OpenAI policy pages by hand~~ Done 2026-10-31 (OpenAI section). Still unread: the OpenAI Data
   Processing Addendum, Service Credit Terms, Sharing and Publication Policy and Brand Guidelines, and the
   platform.openai.com API docs for the rule 8 check. Confirm the Terms of Use copy was the current version.
3. Read the last part of the Claude Code hooks page and the MCP page's remaining text in a browser (the second
   pass covered them only by summary and keyword scan), and the Data Processing Addendum.

## Questions for a lawyer

1. A subscription user who runs `trove setup` and starts Claude Code with no API key has their OAuth token sent
   to the local gateway before it is refused. Trove does not log, store or forward it. Does merely *receiving* it
   on `127.0.0.1` fall under "collect, store, or intermediate Claude.ai credentials or session tokens"?
   (First raised in [legal-and-credentials.md](legal-and-credentials.md). A mitigation was proposed there:
   refuse `trove setup` unless there is evidence of an API key. Implemented 2026-11-06 for the setup path; a user who sets
   `ANTHROPIC_BASE_URL` by hand, or logs in with Claude.ai after setup, still sends the token first.)
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
   a product name or logo. OpenAI's Brand Guidelines were not read; its Services Agreement section 10 bars
   customers from using OpenAI's name or logo in marketing without permission.)
7. OpenAI's Usage policies (effective 2026-11-20) require prior approval for "a model-access intermediary
   platform or other service that enables third parties to directly access OpenAI models". Does a local,
   loopback-only gateway used by the key's owner count as such a service? Our reading is no; confirm, or ask
   OpenAI.

## Update 2026-11-04: every claude.ai login, cloud backends, open-weight servers

**Date checked: 2026-11-04.** Same caveats as above: not legal advice; what each page says, what Trove does, and what
is not verified. Pages were read in full unless noted.

### Sources read today

| Source | What it settled |
|---|---|
| [Claude Code: Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance) | Which plans use OAuth; third-party restrictions; HIPAA scope |
| [Authentication](https://code.claude.com/docs/en/authentication) | Credential types, precedence, `claude setup-token`, Console OAuth and profiles |
| [LLM gateway overview](https://code.claude.com/docs/en/llm-gateway) and [gateway protocol](https://code.claude.com/docs/en/llm-gateway-protocol) | What a base URL without a credential variable does; headers; Bedrock and Vertex API formats. The protocol page (51 KB) was read through targeted search of the saved page, not line by line |
| [Google Vertex AI](https://code.claude.com/docs/en/google-vertex-ai), [Microsoft Foundry](https://code.claude.com/docs/en/microsoft-foundry), [Amazon Bedrock](https://code.claude.com/docs/en/amazon-bedrock) | Per-backend credentials and gateway variables |
| [API credits for Max, Ultra and Team](https://platform.claude.com/docs/en/about-claude/api-credits-for-subscribers) and support article [15036540](https://support.claude.com/articles/15036540) | What the credits cover. The support article was seen only as a search-result summary and was **NOT READ** |
| [AWS SigV4: create a signed request](https://docs.aws.amazon.com/IAM/latest/UserGuide/create-signed-request.html) and [signing elements](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-signing-elements.html) | What a signature covers |
| [InvokeModelWithResponseStream](https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_InvokeModelWithResponseStream.html) | Bedrock streaming format |
| [vLLM OpenAI-compatible server](https://docs.vllm.ai/en/latest/serving/online_serving/), [SGLang docs index](https://docs.sglang.io/) | Endpoints only. vLLM `--api-key` handling and `stream_options` usage behavior were **NOT VERIFIED** (the pages fetched did not cover them) |
| DeepSeek, Moonshot (Kimi), Zhipu (GLM) | **NOT VERIFIED against official docs.** Only third-party summaries were found. Users supply their own `base_url`; Trove ships no defaults for these |

### 1. Every claude.ai login is refused

- *Says:* "OAuth authentication is intended exclusively for purchasers of Claude Free, Pro, Max, Ultra, Team, and
  Enterprise subscription plans". "Anthropic does not permit third-party developers to offer Claude.ai login into
  their own applications, or to route requests through Free, Pro, Max, or Ultra plan credentials on behalf of
  their users. Moreover, developers may not collect, store, or intermediate Claude.ai credentials or session
  tokens." (Legal and compliance.) The routing sentence names Free, Pro, Max and Ultra; Team and Enterprise are
  named in the OAuth sentence only. Trove's rule 10 (reject all of them) is stricter than the text and does not
  depend on that difference.
- *Says:* all plans log in through claude.ai OAuth (`/login`), or `claude setup-token` for a one-year token used as
  `CLAUDE_CODE_OAUTH_TOKEN` that "requires a Pro, Max, Ultra, Team, or Enterprise plan". Setting only
  `ANTHROPIC_BASE_URL` leaves the saved login active and the request carries an OAuth value in `anthropic-beta`
  (Authentication; LLM gateway). Console "keyless" sign-in and Workload Identity Federation profiles are also OAuth
  bearer credentials.
- *Trove:* every route refuses (HTTP 401, never forwarded) a request with an OAuth `anthropic-beta` value, a
  credential beginning `sk-ant-oat` (that prefix is **an assumption**: not documented on the pages read, kept only
  as a second signal beside the beta flag), or, on the Anthropic route, a bearer token with no `x-api-key`. The
  Enterprise or Team plan itself is not a problem: an organization's Console API key is an ordinary key and is
  accepted. The message says gateway mode needs an API key or cloud credential, and that to use a subscription the
  user should remove the custom endpoint (`trove undo`) and use the Trove plugin.
- *Side effect:* a Console OAuth sign-in or WIF profile (not a claude.ai login) is also bearer-only and is refused
  with the same message. Use an API key for the gateway.
- *Tests:* `auth_test.go` (`TestClaudeLoginsRefusedEverywhere`, `TestCloudRoutes`), `routes_test.go`
  (`TestClaudeLoginIsRefusedOnEveryRoute`), `leak_test.go` (setup-token case: nothing reaches logs, responses or
  disk).

### 2. Other providers' subscription tokens (decision)

- *Decision (2026-11-04):* a provider's subscription token is refused unless that provider's terms explicitly allow
  third-party use. The OpenAI section above found no such permission, so a ChatGPT login is refused: a JWT-shaped
  bearer token (heuristic, unchanged) or a `chatgpt-account-id` header on `/openai`, and the header on
  compatible-server routes. Source: the OpenAI and Codex CLI sections above (checked 2026-10-30, not re-read
  today).
- *Not decided here:* any other provider with a subscription login (the gateway supports none).

### 3. Max, Ultra and Team API credits

- *Says:* Max $100, Ultra $200, Team $20 per Standard seat and $100 per Premium seat, pooled and capped at $500 a
  month. Claim by linking a Console organization to the plan. Covers the Claude API, Claude Managed Agents, the
  Claude Agent SDK and the playground; does not cover "Claude Code, extra usage in the Claude apps"; the credits
  "apply only to the Claude API in the Claude Console. They can't be used on other platforms" (Bedrock, Vertex,
  Foundry). Free, Pro and Enterprise are not eligible.
- *Discrepancy with the brief:* the brief lists `claude -p` as covered. The credits page does not mention `claude -p`
  and lists "Claude Code" as not covered. A search result summarizing support article 15036540 says Agent SDK and
  `claude -p` usage still draws from subscription limits after a paused change. Trove therefore says only what the
  page says, and `trove setup` tells users to confirm `claude -p` on their Console billing page.
- *Trove:* `trove setup` explains the option. Sessions are labeled `interactive`, `automated` or `unknown` from the
  `X-Trove-Kind` header the user sets. Trove does not guess from the User-Agent: no documented signal separates
  `claude -p` from an interactive session (the documented `x-claude-code-request-class` header describes the role of
  a request, not the session mode). The report shows kinds in a separate table and says it cannot see whether
  credits were used. `X-Trove-Kind` is removed before the request is forwarded.

### 4. Cloud backends

| Backend | Credential | Body signed? | Gateway behavior |
|---|---|---|---|
| Vertex (`upstreams.vertex`, route `/vertex`) | Google OAuth bearer token (ADC) | No | Forwarded. Opt-in optimizers may change the body. No list price on file, so cost stays unknown |
| Foundry (`upstreams.foundry`, route `/foundry`) | API key (`x-api-key` or `api-key`) or Entra ID bearer | No | Same as Vertex. The configured base URL keeps its `/anthropic` path |
| Bedrock (`upstreams.bedrock`, route `/bedrock`), Bedrock API key | `Authorization: Bearer` | No | Forwarded. Streamed usage is read from the AWS event stream on a side copy; the stream is forwarded byte for byte |
| Bedrock, AWS SigV4 | `Authorization: AWS4-HMAC-SHA256 ...` | **Yes** | Refused with an explanation. Not forwarded |

- *Says (AWS):* the canonical request that SigV4 signs includes `HashedPayload` (`Hex(SHA256Hash(payload))`) and
  the `host` header ("You must include the host header (HTTP/1.1) ... in the signature"). Any body change breaks
  the signature. A client that signs for `127.0.0.1:8787` also produces a signature AWS will not accept for its own
  host, so even an unmodified pass-through should fail.
- *Decision:* no observe-only SigV4 mode, because by the reasoning above it cannot work, and Trove will not hold AWS
  credentials to re-sign (the brief withholds that approval). This refines the earlier choice "observe-only for
  SigV4". **Not verified live:** that AWS rejects a Host-rewritten pass-through of a SigV4 request. Confirm on a real
  AWS account before treating it as settled.
- *Says (Claude Code docs):* a Bedrock gateway must forward the response body and `Content-Type`
  (`application/vnd.amazon.eventstream`) unmodified. Trove does.
- *Not verified:* the binary framing of the event stream (12-byte prelude with CRC-32, headers, payload, CRC-32).
  The AWS page for the streaming API describes `chunk` events carrying `bytes` and the content type, but not the
  framing. `usage/eventstream.go` follows the widely documented encoding and is tested against hand-built frames
  only. Capture a real response to confirm (`docs/gateway-manual-test.md`).
- *Not live-tested:* the Vertex, Foundry and Bedrock routes were tested against fake servers and fixtures only.
  Vertex bodies carry no `model` field, so optimizers that need the model skip those requests and they go through
  unchanged.

### 5. OpenAI-compatible servers (self-hosted and open-weight)

- *Says (Claude Code docs):* Anthropic "doesn't support routing Claude Code to non-Claude models through any
  gateway". The compatible-server route therefore serves OpenAI-format agents (Codex CLI, OpenCode, custom agents),
  and the docs say Claude Code is not supported against it.
- *Trove:* `[[upstreams.compat]]` entries, reached at `/compat/<name>/...`. The agent's own bearer key is forwarded
  and never stored. `auth = "none"` accepts requests without a key, for open servers. Plain `http` is allowed only
  for loopback and private-network hosts, with a warning in the log. Usage is read from Chat Completions
  responses. A streamed reply carries usage only when the client asks for it (`stream_options.include_usage`);
  otherwise the row records no tokens rather than an estimate.
- *Pricing:* `input_per_mtok`, `output_per_mtok` and optional `cache_read_per_mtok` are the user's own numbers. Rows
  say `custom:<hash>` and every dollar figure is labeled estimated. `gpu_hourly_usd` gives a GPU-hour estimate in the
  report: hourly cost divided by measured tokens, once over busy time and once over the whole first-to-last request
  span. It is estimated, depends on the user's hourly number and on which seconds are counted, and is not stored per
  request.
- *Not verified:* vLLM and SGLang usage fields under streaming, and any provider-specific field names.

### 6. Healthcare (BAA)

- *Says:* the BAA extends to Claude Code in a HIPAA configuration (Claude for Enterprise, CLI or Desktop Code tab)
  or with Zero Data Retention. "Under the HIPAA configuration, cloud sessions, Remote Control, Claude Code in the
  Claude mobile app, and Claude Code routed through a third-party cloud provider or a gateway aren't covered."
- *Trove:* `trove setup` says organizations relying on a BAA should use the plugin only, not the gateway.

### 7. The OpenCode plugin: Copilot and ChatGPT logins (decision, 2026-11-05)

The plugin (`plugins/opencode`, [opencode.md](opencode.md)) is not the gateway: it sees no credential, and the
gateway's refusal rule does not apply to it. What it may do for each login is therefore its own decision.

- *Sources read 2026-11-05:* GitHub changelog 2026-01-16 "GitHub Copilot now supports OpenCode"; OpenCode's provider
  docs (opencode.ai/docs/providers); OpenAI's developer docs on signing in with a ChatGPT plan in open-source apps
  (developers.openai.com/siwc/token-sharing-open-source and its preview-limitations page); Anthropic's Claude Code
  legal-and-compliance page and Consumer Terms section 3. **Not readable:** OpenAI's help article "Using your ChatGPT
  plan in other apps and sites" (HTTP 403 to automated reads), OpenAI's Terms of Use, Services Agreement and Usage
  Policies. GitHub's Copilot product-specific terms and generative AI services terms were not read.
- *GitHub Copilot:* GitHub says it "is officially supporting using your Copilot Pro, Pro+, Business, or Enterprise
  subscription with OpenCode". The plugin may change content for a Copilot login (tool level).
- *ChatGPT:* OpenCode's docs describe the login and OpenAI's developer docs describe a "Sign in with ChatGPT" program
  for open-source and locally hosted apps (a preview, with request-shape limits). The primary page that would name
  OpenCode and the terms could not be read, and Business eligibility is unclear. **Decision: shadow-only** (count,
  change nothing) until they have been read and confirm it. This does not contradict section 2: that is about a
  gateway holding the token; here the plugin never sees one.
- *Claude Pro/Max:* not allowed in OpenCode (Anthropic's page); Trove does nothing for it.
- *Plugins that change content:* none of the texts read addresses a third-party plugin that modifies prompts or tool
  results while the user signs in normally. Trove's documents must say "not addressed in the texts reviewed", never
  "permitted". Open question for a lawyer, added below.
- *How the rule is enforced:* the plugin passes a three-valued login label; the engine changes content only for
  `api_key` and `copilot`, lowers everything else to shadow, and returns request bodies untouched. Tests:
  `internal/adapter/plugin/harness_test.go` (`TestLoginsThatMayNotBeChangedAreShadowOnly`),
  `plugins/opencode/test/guard.test.ts`, `plugins/opencode/test/adapters.test.ts`.

### New open items

0. For the OpenCode plugin: read OpenAI's help article 20001542, Terms of Use and Services Agreement before enabling
   ChatGPT logins; read GitHub's Copilot product-specific terms; ask whether a plugin that rewrites prompts or tool
   results is within each provider's terms (none says).
1. Confirm with a real Amazon Bedrock account that SigV4 requests cannot pass through a proxy that changes Host.
2. Capture one real Bedrock streamed response and run it through `usage/eventstream.go`.
3. Check vLLM `--api-key` and streaming usage, and each open-weight provider's official docs, before documenting
   per-provider setup.
4. Confirm the `sk-ant-oat` prefix against a redacted real token, or remove that signal (the OAuth beta flag and
   the bearer-only rule already cover the documented cases).
5. Decide whether `x-claude-code-*` headers can ever support an honest interactive vs automated label (none is
   documented today).

## Update 2026-11-06: terms re-read, code review against the data and provider rules, naming

Not legal advice, and no legal conclusions are drawn. This update re-reads the provider terms, records the
clauses that matter for CLAUDE.md rules 3, 10, 11, 12, 13 and 14 word for word, and records what a review of the code
found. Quotes are exactly as the fetch returned them; each is at most 125 characters, so longer clauses are split
with "..." and the page is the authority. Quotes were taken from one fetch each and have not been re-read by a
second person.

### Sources read today

| Source | Stated date on the page | Read? |
|---|---|---|
| [Claude Code: Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance) | none | Yes, in full |
| [Usage Policy](https://www.anthropic.com/legal/aup) | Effective **Nov 12, 2026** (six days after today; version history is linked from the page) | Yes, in full |
| [Consumer Terms of Service](https://www.anthropic.com/legal/consumer-terms) | Effective **Oct 8, 2025** | Yes, in full (was NOT READ before) |
| [Commercial Terms of Service](https://www.anthropic.com/legal/commercial-terms) | Effective **Jun 17, 2025** | Yes, in full |
| [Trademark guidelines](https://www.anthropic.com/legal/trademark-guidelines) (the URL `/legal/trademark` returns 404) | Effective **Aug 1, 2024** | Yes, in full (was not read before) |
| [Data Processing Addendum](https://www.anthropic.com/legal/data-processing-addendum) | Effective **Feb 24, 2025** | Yes, in full; the subprocessor list it links to was not fetched |
| [GitHub Terms of Service](https://docs.github.com/en/site-policy/github-terms/github-terms-of-service) | Effective **Apr 27, 2026** | **Partly**: sections A through the start of L.3 verbatim; L.3 to S were only summarised by the fetch tool and nothing from them is quoted |
| [GitHub Acceptable Use Policies](https://docs.github.com/en/site-policy/acceptable-use-policies/github-acceptable-use-policies) | none stated | Yes, in full |
| OpenCode [license](https://github.com/anomalyco/opencode/blob/dev/LICENSE) (MIT; the old `sst/opencode` address now resolves to this repository) | "Copyright (c) 2025 opencode" | Yes, in full |
| OpenCode [Terms of Service](https://opencode.ai/legal/terms-of-service) | Effective **Aug 15, 2026** | Yes, in full. Its Privacy Policy (effective Nov 6, 2026) and brand page (`/brand`) were not fetched |
| OpenAI [Terms of Use](https://openai.com/policies/terms-of-use), [Usage policies](https://openai.com/policies/usage-policies), [Services Agreement](https://openai.com/policies/services-agreement) | unknown | **NOT READ: HTTP 403** to automated fetches. The 2026-10-31 reading (copied from a browser) is the latest we have. Needs a person to paste the current pages |
| Codex authentication docs | unknown | **NOT READ.** `developers.openai.com/codex` redirects to a different host (`learn.chatgpt.com`); only its front page came back and nothing from it is used here. The sign-in and authentication pages were not fetched |

What changed since the 2026-10-30 to 2026-11-04 checks: the Consumer Terms, the Trademark guidelines and the DPA were
read for the first time. The Usage Policy and Commercial Terms dates are unchanged. Nothing read contradicts what the
earlier sections of this file say Trove does. Not yet verified: the current OpenAI pages, which may have changed.

### Clauses, verbatim

These are the clauses a reader should look at first. Trove makes no claim that it complies with them; that is for the
lawyer questions below.

**Competing products and services**

| Source | Clause |
|---|---|
| Consumer Terms, section 3 "Use of our Services", item 2 | "To develop any products or services that compete with our Services" ... "including to develop or train any artificial intelligence or machine learning algorithms or models or resell the Services." |
| Commercial Terms, section D.4 "Use Restrictions" | "Customer may not and must not attempt to (a) access the Services to build a competing product or service, including to train competing AI models" ... "or resell the Services except as expressly approved by Anthropic" |
| OpenCode Terms, "Are there restrictions in how I can use the Services?", item 7 | "uses Output to develop artificial intelligence models that compete with the Services or any Third Party Models;" |
| Usage Policy, GitHub terms and GitHub AUP, Claude Code legal page, Trademark guidelines, DPA | no competing-product clause found in the text read |

**Training AI models**

| Source | Clause |
|---|---|
| Usage Policy, "Do Not Abuse Our Platform" | "Use outputs to train an AI model (e.g., "model scraping" or "model distillation") without prior authorization from Anthropic" |
| Consumer Terms, section 3, item 2 | "to develop or train any artificial intelligence or machine learning algorithms or models" (same clause as above) |
| Commercial Terms, section D.4(a) | "to train competing AI models" (same clause as above); section D.2 incorporates the Usage Policy |
| OpenCode Terms, item 7 | as above |
| GitHub Terms, section D.9 "Access Reciprocity" | "By using automated means to access, collect, or otherwise use" ... "for the purpose of developing or training any commercially available artificial intelligence model". |
| GitHub Terms, section J.3 | A licence granted *to GitHub* to use Inputs and Outputs to "develop, train and improve" its models; not a restriction on users |
| Anthropic's own side (for context) | Commercial Terms, section B: "Anthropic may not train models on Customer Content from Services."; Consumer Terms, section 4: "We may use Materials to provide, maintain, and improve the Services and to develop other products and services, including training our models, unless you opt out of training through your account settings." |

**Why Trove's use of metadata is a different thing (flagged for lawyer review, no conclusion).** Trove trains no model on
model output, generated code or text (CLAUDE.md rule 12). Where Trove learns from data, the inputs are metadata,
derived features and outcomes: token counts, sizes, hashes, fixed labels, pass/fail. `internal/recommend` is rules and a
lookup table with no model; `internal/features` produces only numbers, booleans and fixed vocabularies
(`TestNoFeatureCanHoldText`); `internal/tuning` stores one number per repo parameter. The opt-in summary holds counts
and percentages (see [data.md](data.md)). Benchmark transcripts are redacted, git-ignored and used only for analysis
and debugging ([benchmark-methodology.md](benchmark-methodology.md)). Whether a model trained on such metadata could
fall under any clause above is a question for a lawyer (questions 8 and 9 below).

**Automated access, reselling, intermediaries and credentials**

| Source | Clause |
|---|---|
| Claude Code legal page, "Can customers offer Claude Code in their products?" | "Customers may not pay for, resell, or intermediate Claude usage on their end users' behalf." |
| Claude Code legal page, "Authentication and credential use" | "OAuth authentication is intended exclusively for purchasers of Claude Free, Pro, Max, Ultra, Team, and Enterprise" |
| same | "Anthropic does not permit third-party developers to offer Claude.ai login into their own applications" |
| same | "developers may not collect, store, or intermediate Claude.ai credentials or session tokens" |
| same | "The Claude Code binary must not be modified." and "customers may not remove, disable, or restrict any authentication method built into it" |
| Usage Policy, "Do Not Abuse Our Platform" | "Resell, proxy, or otherwise provide access to Claude through unauthorized means" ... "including services that route requests through consumer subscriptions or misrepresent the product or client being used" |
| Usage Policy | "Intentionally bypass or interfere with capabilities, restrictions, or guardrails established within our products, including by circumvention of the account sign-up or log-in process" |
| Consumer Terms, section 3, item 7 | "Except when you are accessing our Services via an Anthropic API Key or where we otherwise explicitly permit it" ... "to access the Services through automated or non-human means, whether through a bot, script, or otherwise." |
| Consumer Terms, section 2 | "You may not share your Account login information, Anthropic API key, or Account credentials with anyone else." |
| Consumer Terms, section 3, item 3 | "To decompile, reverse engineer, disassemble, or otherwise reduce our Services to human-readable form" |
| Commercial Terms, section D.4 | "(b) reverse engineer or duplicate the Services" |
| GitHub Terms, section B.3 | "Your login may only be used by one person" ... "a single login may not be shared by multiple people." |
| GitHub Terms, section H "API Terms" | "You may not share API tokens to exceed GitHub's rate limitations." |
| OpenCode Terms, restrictions item 5 | "automatically or programmatically extracts data or Output (defined below);" |
| OpenCode Terms, "What are the basics of using OpenCode?" | "You will only use the Services for your own internal use, and not on behalf of or for the benefit of any third party," |
| OpenCode Terms, "What about Third Party Models?" | "Your ability to access Third Party Models is contingent on you having API keys" |
| OpenAI (pages not readable today) | Last read 2026-10-31: see the OpenAI section above and question 7 |

**Names and logos**

| Source | Clause |
|---|---|
| Claude Code legal page, "Using the Claude Code name and logo" | "you can't use the Claude Code or Anthropic names or logos as part of your own product, feature, or company name" |
| same | "in a way that suggests Anthropic built, endorses, or is partnered with your product." and "You can accurately say, in plain text, that your product has Claude Code preinstalled or that it runs Claude Code." |
| Consumer Terms, section 12 "Use of our brand" | "You may not, without our prior written permission, use our name, logos, or other trademarks in connection with products or services other than the Services" |
| Trademark guidelines | "You may only use our trademarks as specifically permitted by us and only in materials we approve beforehand." and "You may not use our trademarks in a manner that implies Anthropic's sponsorship or endorsement" |
| GitHub Terms, section G.2 | "you must follow all of our trademark guidelines, including those on our logos page" |
| OpenCode Terms colophon | "Marks: the OpenCode logo, wordmark and font." |

### What a review of the code found (2026-11-06)

Every item below is a reading of the code and its tests, with file references. "None found" means none found by
searching for it, not a proof of absence.

| Question | Result |
|---|---|
| Handling of subscription credentials or session tokens | The gateway classifies every request in one place, `internal/gateway/auth/auth.go`, called at `internal/gateway/proxy.go:66` before the body is read or anything is forwarded. It refuses claude.ai logins (an `anthropic-beta` value containing "oauth"; a bearer with no `x-api-key`; a different bearer; the `sk-ant-oat` prefix), JWT-shaped OpenAI bearers, and Bedrock SigV4. The reply is HTTP 401 with a message that names all claude.ai plans and ChatGPT logins. Credentials are not in the header allowlist (`proxy.go:285`) that later code sees; the log line on refusal holds the provider and a credential-free reason. Trove never opens `~/.claude.json`, `.credentials.json` or `auth.json` (they are on the refuse lists in `internal/fixer/paths.go` and `internal/mcp/root/sensitive.go`). **Unresolved:** a subscription user who sets only `ANTHROPIC_BASE_URL` sends their OAuth token to the local gateway before the 401 (question 1). The `sk-ant-oat` prefix and the JWT shape are assumptions (open item 4 above) |
| Scraping of provider web pages | None found. No usage-meter scrapers, cookies or browser automation. The only fetches of provider pages are the maintainer script `scripts/pricing-update.sh`, which downloads official docs pages as text for a person to read and never edits prices, and the bench Docker image, which runs the vendor's own install script |
| Decompiling or probing agent internals beyond documented interfaces | No decompiling, binary reading or `strings` on binaries found. `trove hooks probe --live` (`internal/hooks/probe`) runs the user's own `claude -p` with documented flags to test whether hook rewrites take effect, and is refused unless there is evidence Claude Code uses an API key (2026-11-06). `trove hooks verify` tests the same thing in a session the user starts, with no Trove-started request. `internal/logs/cchistory` reads Claude Code's session files read-only (`docs/logs.md`; the schema was confirmed by inspecting local files, which is observation of an unstable format). `internal/logs/cxhistory` reads Codex rollout files whose format OpenAI does not document (its header says "UNVERIFIED ON REAL INSTALLS"); this is the weakest item |
| Model output used to train a model | None found. No training or fine-tuning code. Bench transcripts (`agent_stdout.txt` and friends) are redacted, written under `bench/results/` which is git-ignored, and read only by humans and the report. The recommender is rules (`internal/recommend/recommend.go`). A future `Provider` interface for a hosted settings bundle is mentioned in a comment and not implemented |
| Resale or intermediation of usage | None found. The gateway binds to loopback only (`internal/cli/gateway.go`), forwards the caller's own key, and holds no key of its own; it refuses to hold AWS credentials to re-sign requests. `trove probe --live` spends the user's own money under a cap they set |
| Outbound calls | Only four allowlisted files may reach the network, enforced by `internal/egress/static_test.go`: the gateway's forwarding to the configured upstream (`gateway/proxy.go`, `gateway/server.go`), a loopback-only health probe (`cli/gateway.go`), the opt-in summary upload and the one delete request only `trove contribute delete` makes (`contribute/upload.go`; CLAUDE.md rule 1, parts b and c, amended 2026-11-06). On Linux a second test traces real commands with strace; it does not run on macOS or Windows. See [data.md](data.md) |

### Naming (rule 13)

Checked by `internal/naming/naming_test.go`, which walks every folder and file name, the command tree, the package and
plugin manifests, the MCP tool names and the subagent names, and fails if a new one contains Claude, Anthropic, Codex
or OpenAI. It runs in CI.

Changed today, because the rule lists "package": `internal/logs/claudecode` is now `internal/logs/cchistory`,
`internal/logs/codex` is `internal/logs/cxhistory`, `internal/hooks/probe/codex.go` is `cxcaps.go`,
`internal/fixer/codexmodel_test.go` is `cxmodel_test.go`, and `bench/trovebench/agent/claude_code.py` is `cc.py`
(class `CcAgent`).

Clean already: the Go module `github.com/usetrove/trove`, the plugin and marketplace name `trove`, the npm package
`trove-cli`, the OpenCode plugin `trove-opencode`, the pip package `trovebench`, every `trove` subcommand, the MCP tools
(`trove_read`, `trove_search`, `trove_run`, `trove_fetch_full`), the subagents (`trove-scout`, `trove-cascade`) and the
plugin commands.

Kept on purpose, each a name the integrated agent or provider dictates or a plain description of what is integrated
(decision 2026-11-06; for lawyer review, question 6 above):

- `.claude-plugin/` and `CLAUDE.md`: names Claude Code's own formats require.
- Gateway route prefixes `/anthropic` and `/openai`, config sections `upstreams.anthropic` and `upstreams.openai`, the
  environment variables `TROVE_UPSTREAMS_ANTHROPIC` and `TROVE_UPSTREAMS_OPENAI`, and `--provider anthropic|openai`:
  they name the provider API being forwarded to. Renaming them would break installed agent settings
  (`ANTHROPIC_BASE_URL` points at `/anthropic`).
- Agent identifiers used as data values (`claude_code`, `codex`, `claude-code`, and setup/undo ids such as
  `claude_code_hooks`) and the files that list the agents' documented settings
  (`internal/settings/catalog/*.toml`, `internal/tasks/catalog/claude_code.toml`).
- Parsers and fixtures named for the API they read (`internal/gateway/usage/anthropic.go`, `openai.go`, `testdata/`).
- Identifiers inside code (for example `logs.SourceClaudeCode`) and documentation page names.

### New open items (2026-11-06)

1. Paste the current OpenAI Terms of Use, Usage policies and Services Agreement, and the Codex sign-in and
   authentication pages, into a file for review (automated fetches get HTTP 403 or a redirect to another host).
2. Read GitHub Terms of Service sections L.3 to S, the OpenCode Privacy Policy and its brand page.
3. The Usage Policy's effective date (Nov 12, 2026) is six days away. Re-read it on or after that date.
4. ~~The mitigation for question 1~~ Done 2026-11-06: `trove setup` refuses to point Claude Code at the gateway without evidence of an API key (`internal/cli/setup.go` `claudeNeedsAPIKey`). The residual case (endpoint set by hand) remains in question 1.
5. ~~`trove hooks probe --live` under a subscription login~~ Done 2026-11-06: `--live` is refused unless there is evidence of an API
   key (`internal/hooks/probe` `Options.APIKeyAuth`; the CLI uses the same evidence as `trove setup`). Subscription users get
   `trove hooks verify`, where the user starts the session and Trove starts no model request, so the only place Trove itself
   starts model requests is now API-key only. Residual: the evidence is a heuristic, like the gateway's (a stale `ANTHROPIC_API_KEY`
   in the shell with a Claude.ai login in use elsewhere would pass it).
6. The Codex session-file reader depends on an undocumented format; verify on a real install or remove it.
7. Retention gap closed today: before this update only six tables were pruned and only while the gateway ran; see
   [data.md](data.md).

### Questions for a lawyer (added 2026-11-06; 1 to 7 above still stand)

8. Trove stores no model output and trains nothing on content. Could any "train" or "develop AI models" clause quoted
   above apply to a model trained only on metadata (token counts, sizes, fixed labels, pass/fail) about a user's own
   sessions?
9. The Usage Policy clause is about outputs. Is a count of tokens in an output, or a pass/fail on code an agent wrote,
   "using outputs"?
10. The benchmark lab runs a provider's agent on public coding tasks with its own API key and keeps redacted transcripts
    for analysis only. Is publishing aggregate results (costs, pass rates) within each provider's terms?
11. Is the opt-in summary (see [data.md](data.md)) "personal information" or "personal data" under PIPEDA, BC PIPA,
    GDPR or US state laws, given a random install id and coarse counts? See [privacy-law.md](privacy-law.md).
12. For a future team edition: what notice, consent and aggregation does employee-monitoring law require in each region?
    See [privacy-law.md](privacy-law.md).
13. ~~Does the "automated or non-human means" ban apply to `trove hooks probe --live` under a Pro or Max login?~~ No longer needed: that
    path is refused without an API key. Still worth asking: is a hook that rewrites one tool call in a session the user started
    (`trove hooks verify`) within the terms?

### Questions for Anthropic

1. May a third-party local tool read the session files Claude Code writes under `~/.claude/projects`, read-only, to
   compute usage and cost for the same user? Is that format stable or supported?
2. When a subscription user points `ANTHROPIC_BASE_URL` at a local gateway that immediately refuses the request,
   is receiving the OAuth token on loopback, without logging or forwarding it, within "may not collect, store, or
   intermediate Claude.ai credentials or session tokens"? What do you recommend to prevent it?
3. Is "works with Claude Code" in plain text, in docs and a README, within the Trademark guidelines for a tool that
   also names `ANTHROPIC_BASE_URL` and registers an MCP server?
4. Is a user-started `claude -p` run, a few cents' budget, under their own subscription login to test whether a hook
   rewrite works, within the Consumer Terms on automated access?
5. Is there a documented way for a tool to learn whether a session is API-key or subscription authenticated, instead of
   inferring it from settings and headers?
6. Does an opt-in optimizer that rewrites a user's own API request body, before it reaches the API, raise any issue
   under the Usage Policy or Commercial Terms?
7. Which of the pages above (Usage Policy, Consumer Terms) governs a Team or Enterprise seat's Claude Code use, given
   the Claude Code legal page says Commercial Terms apply to Team, Enterprise and API users?
