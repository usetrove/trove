# OpenCode plugin verification

Run on a real machine; CI cannot start OpenCode. Record the date, the OpenCode version and the login type for each
line. Use a throwaway project and set `TROVE_DATA_DIR` to a fresh folder so the real `~/.trove` is not involved.
Put `noise_normalizer = "on"`, `hook_quiet_commands = "on"` and `hook_read_limit = "on"` in that folder's
`config.toml`, with `[experiment] enabled = false` so every session runs the optimizers for real.

## Automated (`go test ./...` and `cd plugins/opencode && npm test`)

See [opencode.md](opencode.md#tests-and-parity). CI runs the plugin tests and `tsc --noEmit`.

## Unverified facts the plugin depends on

Each of these was read in OpenCode's types or docs, not run. Fill in the result; if one is false, the plugin's
glue (not the engine) is what changes.

### OpenCode 1.x (`opencode-ai`)

- [ ] How a plugin folder is loaded: from `.opencode/plugins/` and from the `plugin` array. Result: ______
- [ ] The plugin loads once and `server` runs (turn on `debug` and look for the start-up of `trove engine serve`)
- [ ] A `bash` call `npm install` reaches the shell as `npm install --quiet` (mutating `output.args` in
      `tool.execute.before` changes what runs). Result: ______
- [ ] A `read` of a file of 40 KB or more with no `limit` runs with `limit: 400`
- [ ] `tool.execute.after` sees a tool's output **before** OpenCode truncates it (a command printing over 2000 lines).
      Result: ______
- [ ] A changed `output.output` is what the model sees, and what is stored in the session history
- [ ] The built-in tool ids are `read`, `bash`, `grep`, `glob` (grep and glob were inferred from file names)
- [ ] `chat.params` fires before the first tool call of a turn, so the login is known. With a key in an environment
      variable the session is `api_key`; with a key saved by `opencode auth login` it is shadow-only
- [ ] `message.updated` with `time.completed` set fires once per assistant message with final `tokens` and `cost`

### OpenCode 2.x (`@opencode/cli`)

- [ ] The package loads from the `plugins` array as a directory path and `setup` runs
- [ ] **Assigning a new `Request` to `event.request` in `session.hook("http.request")` is honoured.** The docs only
      show header changes. Check with a request that has a long, noisy tool result: the provider receives the
      shorter body. Result: ______
- [ ] The request that goes out still carries the user's credential header (it is carried over unread), and the
      content length matches the body
- [ ] The built-in tools' V2 argument names (`filePath`? `command`?) and result shape (`content` string, text parts
      or `output`). Result: ______
- [ ] `ctx.integration.connection.active(providerID)` accepts the provider id (the docs show it taking an
      integration id) and returns `{type: "credential", method: "key" | "oauth"}` or `{type: "env"}`. If it does
      not, every V2 session is shadow-only. Result: ______
- [ ] The shape of the items `ctx.event.subscribe()` yields: is usage under `properties`, `data` or the item
      itself, and does `session.step.ended` carry `tokens` and `cost`? Result: ______

### Both

- [ ] With the plugin's `binary` pointing at a missing file, OpenCode runs normally and nothing is changed
- [ ] Kill the `trove engine serve` process mid-session: the next calls go through unchanged and it restarts
- [ ] Close OpenCode: the `trove engine serve` process exits (no orphan) and `trove report` shows the session
- [ ] `trove report` shows a **Harness plugins** section with the right harness, login and counts, and no cost
      total for a Copilot login
- [ ] `sqlite3 ~/.trove/trove.db 'select * from adapter_events limit 20'` shows hashed session keys and no text

## Logins

- [ ] API key (environment variable): optimizers change content in 1.x and 2.x
- [ ] GitHub Copilot login: tool-level changes apply; no request body is sent to the engine (2.x requires
      `anthropic`); usage shows no cost
- [ ] A ChatGPT login: nothing is changed anywhere (shadow-only); confirm `trove report` shows
      "a login Trove can only watch"
- [ ] An OAuth/unknown login of any provider: same
- [ ] Re-read, before enabling ChatGPT: OpenAI's help article "Using your ChatGPT plan in other apps and sites"
      (it returned 403 to automated reads), the Terms of Use and the Services Agreement. Date: ______

## Overhead

- [ ] `node plugins/opencode/bench/bridge.bench.ts` on the machine you care about; record the table in
      [opencode.md](opencode.md). Inside OpenCode, with `debug` on, time a tool call with and without the plugin.
