# Third-party tools (`trove external`)

Trove can wrap token-saving tools that other people make, and judge them the way it judges
its own optimizers: by the real bill and by whether your work still goes well. Nothing is
installed without you.

```
trove external list                 # what Trove can wrap
trove external show rtk             # license, exact command, overhead, the tool's own claim
trove external install rtk          # shows all of that, then asks (needs a terminal, no --yes)
trove external enable rtk           # takes effect for sessions you start afterwards
trove external disable rtk          # undoes the config change; the tool stays installed
trove external remove rtk           # disable + uninstall + forget
trove external status               # installed tools, their state, config problems
trove report                        # "Third-party tools" section: claimed vs measured
```

Every command supports `--json`.

## The three tools shipped today

Verified on 2026-11-04 from each project's own README, LICENSE and release or package page.

| Tool | Version | License | How it acts | Wired by |
| --- | --- | --- | --- | --- |
| `rtk` | 1.1.0 (git tag `v1.1.0`) | Apache-2.0 (Apache-only build) | shell output compressor | a `PreToolUse` Bash hook that Trove writes into `~/.claude/settings.json` |
| `token-savior` | 4.24.0 (PyPI `token-savior-recall`) | MIT | MCP server for code navigation | `claude mcp add --scope user`, run by Trove; Trove never edits `~/.claude.json` |
| `terse-rules` | 1.0.0 | Trove project | a short rules block | a fenced block in `~/.claude/CLAUDE.md` |

Points worth knowing before you install:

- **RTK** has two builds. Trove installs the Apache-2.0-only build (`--no-default-features`),
  not the default build, which adds a BUSL-1.1 engine. RTK's own savings claim may assume the
  default build. Another project named `rtk` exists on crates.io; Trove installs only from
  `github.com/rtk-ai/rtk` at the pinned tag. Only shell calls pass through it, not the
  agent's built-in Read, Grep or Glob.
- **token-savior** shares its name with other projects. The PyPI project's listed repository
  is `github.com/Mibayy/token-savior`. Its PyPI uploads carry no provenance and do not use
  Trusted Publishing, and the PyPI maintainer account differs from the GitHub owner. Its
  README says the benchmark source is not published. Trove shows these cautions at install.
- **terse-rules** is not a third-party tool; it is the example of a rules file. The model may
  not follow rules, so it is judged like everything else.

## States

External tools are `off` or `on`. None of the first three can run in `shadow`, because they
act inside the agent's own config, outside Trove's request path, so Trove cannot estimate what
they would do without doing it. Asking for `shadow` is refused with that reason. The only way
to evaluate them is to compare sessions with the tool on against sessions with it off. That is
a comparison between periods, not interleaved sessions, so it is weaker evidence, and the report
says so.

Like built-ins, a change applies to sessions started afterwards. Running sessions keep what they
had, and the gateway records each session's real tool state when it starts.

## Licenses

`install` shows the license. Licenses that restrict commercial or company use (PolyForm
Noncommercial, Elastic License v2, BUSL, SSPL, CC BY-NC) and licenses Trove does not recognise
print a `WARNING` and require you to type the tool's name. Trove cannot tell whether your use is
allowed; it only makes sure you looked.

## Overhead

A tool is charged for what it adds to every request:

- **Tool definitions** (MCP servers): declared by the tool, labeled estimated.
- **Injected rules**: counted from the rules text itself, never trusted from the manifest.
- **Extra turns**: not added up separately. They show up in what the session costs.

The measured comparison uses provider-reported prompt tokens and the real cost per session, which
already contain all three. Trove therefore does not subtract the declared overhead a second time
from a measured figure. The declared number is shown beside it so you can see where it comes from.

## Rollback

When a tool is on, Trove compares the cost per session with the tool on against sessions without
it (the gateway checks every 5 minutes; hook bookkeeping checks too). If sessions with the tool
cost more, beyond what chance explains, Trove switches it off for new sessions, undoes its config
change, records a `net_cost` guardrail event, and says why in the report. The tool stays
installed. Re-enable it deliberately if you disagree.

## Conflicts

Two tools in the same exclusive category (`shell_compressor`, `code_nav`, `behavior_rules`) cannot
be on together, and neither can a shell compressor and an overlapping built-in (`noise_normalizer`,
`output_trimmer`, `hook_large_output`). `enable` refuses and names what to turn off. `trove external
status` also reports a hand-edited config that breaks these rules.

## What the report shows

For each installed tool: the tool's own claim, quoted and labeled "claimed ... not checked by
Trove"; Trove's measured prompt tokens per request and cost per session, on versus off; the
estimated overhead; the license class; and the reason if Trove switched it off. The claimed and
measured figures are never combined.

## Privacy and safety

- Manifests are built into the binary. Trove does not load install commands from files on disk.
- Install and remove run only the manifest's argv, directly (no shell), after you confirm in a
  terminal. A pipe or an agent cannot confirm.
- Command output in error messages is redacted. `~/.trove` stores the tool name, version, license,
  on/off state, timestamps and the IDs of config changes, nothing else (see PRIVACY.md).

## Known gaps

- Per-session on/off switching is not possible for config-wired tools, so evaluation is
  sequential.
- Hooks-only sessions (no gateway) write no session pins, so they cannot yet be compared. Users
  without the gateway see the claim, overhead and license, but not a measured verdict.
- RTK's hook entry is built from its documentation, not from running it. After a first real
  install, compare it with the output of `rtk init -g --no-patch`.
- `rtk --version` output is unconfirmed; Trove only checks that it contains `1.1.0`.
- External tools are not in the capability matrix (`doctor --capabilities`, `docs/adapters.md`) yet.
- Automatic checks run only while the gateway is running or hooks bookkeeping runs.

## Proposed CLAUDE.md rule (needs your approval; not applied)

> 16. External tools. Trove may run a package manager or the agent's own CLI to install or remove
> a tool, only from a built-in manifest, only after the user confirms at a terminal, never silently
> and never in the gateway request path. Trove itself makes no network call for this; the package
> manager does. Every external tool shows its license, declared overhead and its own savings claim
> (labeled "claimed") before install, and is judged against the real bill like any optimizer.
