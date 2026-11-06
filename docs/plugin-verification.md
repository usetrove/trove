# Plugin verification

Run on a real machine; CI cannot start Claude Code. Record the date, Claude Code version and login type for each
run. Use a throwaway `HOME` (or a spare user) so your real `~/.claude` and `~/.trove` are not involved, and set
`TROVE_DATA_DIR` to a fresh folder.

## Automated (CI and `go test ./...`)

- `go test ./internal/cli -run 'Plugin|Wrapper|HooksInstallRefuses'`: generated files match the committed ones,
  the plugin registers the same handlers as `trove hooks install --rewrite`, no name carries a brand (rule 13),
  commands are user-only, first run reports once and enables nothing, `plugin start` fails open, double
  registration is refused, the status line needs consent and never touches the gateway, the wrapper never
  downloads from a hook and refuses a bad checksum.
- `claude plugin validate plugin/trove --strict --json` (needs Claude Code; run before a release).
- `shellcheck plugin/trove/bin/trove` (CI).

## By hand

Fresh install, Pro or Max subscription login, clean `~/.claude`:

- [ ] `claude plugin marketplace add <this repo>` then `claude plugin install trove@trove`
- [ ] Start Claude Code. Time from start to the report: ______ (target: under 2 minutes, including the program download)
- [ ] The report appears as a message, or `/trove:report` shows it. Which one happened: ______
- [ ] `~/.trove/config.toml` does not exist or has no optimizer set to shadow/on; `trove optimize status` shows all off
- [ ] `~/.claude/settings.json` changed only by `enabledPlugins` (diff it)
- [ ] Hooks fire (`trove hooks status` after a session) and Claude Code works normally with the program missing
      (rename the cached copy, start a session, confirm no error)
- [ ] The same on a Team/Enterprise login and an API-key login (hooks are not documented as login-dependent; confirm)
- [ ] `/trove:setup`: decline everything, confirm nothing changed; accept one item, confirm only it changed
- [ ] `/trove:uninstall`, then `claude plugin uninstall trove`: `~/.claude` is byte-identical to the copy taken
      before step 1 (a plugin uninstall removes the `enabledPlugins` entry)
- [ ] Update Claude Code, start a session: the once-only notice appears if a live probe had passed, and
      `trove plugin status` shows rewrites unsupported
- [ ] Slash command names as Claude Code shows them: ______ (documented as `/trove:report` etc.)
- [ ] Company rollout: put the managed-settings JSON in the OS path, start a clean profile, confirm the plugin
      loads without a prompt and a user cannot disable it
