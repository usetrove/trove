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

## The tools shipped today

Verified on 2026-11-04 (Ponytail and Caveman on 2026-11-17) from each project's own README, LICENSE and release or package page.

| Tool | Version | License | How it acts | Wired by |
| --- | --- | --- | --- | --- |
| `rtk` | 1.1.0 (git tag `v1.1.0`) | Apache-2.0 (Apache-only build) | shell output compressor | a `PreToolUse` Bash hook that Trove writes into `~/.claude/settings.json` |
| `token-savior` | 4.24.0 (PyPI `token-savior-recall`) | MIT | MCP server for code navigation | `claude mcp add --scope user`, run by Trove; Trove never edits `~/.claude.json` |
| `terse-rules` | 1.0.0 | Trove project | a short rules block | a fenced block in `~/.claude/CLAUDE.md` |
| `ponytail` | 5.3.3 (git tag `v5.3.3`) | MIT | rules that make the agent write less code (T2) | a fenced block in `~/.claude/CLAUDE.md` |
| `caveman` | 4.1.0 (git tag `v4.1.0`) | Apache-2.0 | skill text that makes replies terse (T2) | a fenced block in `~/.claude/CLAUDE.md` |

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
- **Ponytail** is wrapped as its rules text only: the project's `AGENTS.md` at tag `v5.3.3`
  (`github.com/DietrichGebert/ponytail`), minus its last line, which only addresses agents
  working on that repository. Its Claude Code and Codex plugins, skill and lifecycle hooks are
  not installed. Its README claims -26% cost and -45% tokens from its own benchmark; an
  independent JetBrains test (July 2026) measured a quarter to half of that, concentrated where
  the agent over-builds. Its MIT notice ships in the binary (`manifests/ponytail.LICENSE`).
- **Caveman** is wrapped as its skill text only: `skills/caveman/SKILL.md` at tag `v4.1.0`
  (`github.com/JuliusBrussee/caveman`), placed in `CLAUDE.md` so it applies to every reply and
  can be compared session against session. The frontmatter and the paragraph about
  `/caveman ultra`, `/caveman wenyan` and `/caveman status` are removed, because those skills and
  commands are not installed; the block's first line says so, as Apache-2.0 asks. Its proxy, CLI,
  Auto mode, account login and other skills are never installed. The project says the rules cost
  about 1,160 input tokens per request, and measured 41% less output than no instruction but only
  3% less than a plain "Answer concisely." (ten questions, its 3.1.0 rules). Its license and NOTICE
  ship in the binary (`manifests/caveman.LICENSE`, `manifests/caveman.NOTICE`). "Caveman" is that
  project's trademark and names the source only.
- **One output-style rule set at a time.** `terse-rules`, `ponytail` and `caveman` share the
  `behavior_rules` slot, so enabling one while another is on is refused and names the one to turn
  off. Stacked rule sets would cost the overhead of each and make the A/B verdict unattributable.
  `verbosity_steer` at the gateway holds the same slot.
- **A per-user trial instead of on-for-everyone.** `trove autopilot trial <rule set>` gives an installed rule set's
  text to a random half of new Claude Code sessions through the SessionStart hook, and reports output tokens per turn,
  cost per turn, test-fail rate and turns against the other half. This is a stronger comparison than the
  period-against-period one below. See [autopilot.md](autopilot.md#rule-set-trials-one-yes-per-rule-set).

## States

External tools are `off` or `on`. None of them can run in `shadow`, because they
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
- **Injected rules**: counted from the whole block written to the rules file (fences and the
  `<!-- trove:managed -->` line included), never trusted from the manifest.
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

## Randomized test for hook tools

For a tool that acts as a Bash hook (RTK, ZTK), Trove does not write the tool's own hook. It writes its own,
`trove hook handoff <tool>`, and hands calls to the tool per session:

- Each new Claude Code session is assigned an arm at SessionStart by a hash of its key, and the arm is recorded (names
  and times only, like every hook trial).
- In the "on" arm the hand-off is transparent: the tool gets the agent's hook input byte for byte, and its stdout,
  stderr and exit code go back to the agent unchanged.
- In the "off" arm nothing runs and the call goes ahead as if no tool were installed.
- A session that began before the test, or whose state cannot be read, keeps the tool, as before. A tool that cannot
  be started lets the call through with no output.

So a hook tool is judged by a randomized session A/B, not by comparing a period before it with a period after, which
mixes in everything else that changed. The comparison is the same hook-trial analysis the scout and cascade use.

Checked live on 2026-11-18 on Claude Code 2.1.327, with a stand-in tool named `ztk` that rewrites `echo probe-N` the
way ZTK rewrites commands: of six sessions, the two the hash put in the "on" arm returned the rewritten output
(`HANDED-OFF-3`, `HANDED-OFF-4`) and the four in the "off" arm returned the original, and the recorded arms matched
each session. The real RTK and ZTK binaries were not run (they install with Homebrew, which this machine lacks).

## Privacy and safety

- Manifests are built into the binary. Trove does not load install commands from files on disk.
- Install and remove run only the manifest's argv, directly (no shell), after you confirm in a
  terminal. A pipe or an agent cannot confirm.
- Command output in error messages is redacted. `~/.trove` stores the tool name, version, license,
  on/off state, timestamps and the IDs of config changes, nothing else (see PRIVACY.md).

## Known gaps

- Per-session on/off switching is possible for hook tools (see "Randomized test for hook tools") but not for
  config-wired ones (rules files), so those are still evaluated before against after.
- Hooks-only sessions (no gateway) write no session pins, so they cannot yet be compared. Users
  without the gateway see the claim, overhead and license, but not a measured verdict.
- RTK's hook entry is built from its documentation, not from running it. After a first real
  install, compare it with the output of `rtk init -g --no-patch`.
- `rtk --version` output is unconfirmed; Trove only checks that it contains `1.1.0`.
- External tools are not in the capability matrix (`doctor --capabilities`, `docs/adapters.md`) yet.
- Automatic checks run only while the gateway is running or hooks bookkeeping runs.

## Proposed PRINCIPLES.md rule (needs your approval; not applied)

> 16. External tools. Trove may run a package manager or the agent's own CLI to install or remove
> a tool, only from a built-in manifest, only after the user confirms at a terminal, never silently
> and never in the gateway request path. Trove itself makes no network call for this; the package
> manager does. Every external tool shows its license, declared overhead and its own savings claim
> (labeled "claimed") before install, and is judged against the real bill like any optimizer.
