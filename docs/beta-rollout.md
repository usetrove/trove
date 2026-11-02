# Beta rollout plan

How Trove goes from "works for the author" to "a stranger can install it from the README". This page is the
plan and the gates. The per-tester questions, metrics and results sheet live in
[beta-checklist.md](beta-checklist.md); the by-hand install checks live in
[install-verification.md](install-verification.md). Nothing here is a result: every cell and threshold that is
blank stays blank until it has been measured or agreed.

Status as of 2026-11-02: release `v0.2.0` is published (macOS and Linux, amd64 and arm64; no Windows build). The
repository and the Homebrew tap are private.

## What the current setup means for a beta

- The checklist's goal is "can a stranger install Trove using only the README?". That cannot be tested while the
  repository, its releases and the Homebrew tap are private: `install.sh` and `brew install` fail for anyone who is
  not invited. Stage 2 therefore needs a public repository and a public tap.
- `trove feedback` builds an issue link for `usetrove/trove` (`internal/feedback/feedback.go`). In a private
  repository, only people with access can open it.
- The install matrix in `install-verification.md` and the exit thresholds in `beta-checklist.md` section 6 are
  blank. They gate Stages 1 and 2.
- The opt-in summary upload exists but few testers will turn it on. Anything learned from it is reported as
  "n = X of Y testers", never as a rate for all users.

## Stages

| Stage | Who | Size | How they get Trove | Gate to move on |
|---|---|---|---|---|
| 0. Dogfood | The author's own machines and 2 to 4 friends who use Claude Code or Codex CLI | 3 to 5 | The `v0.2.0` archive for their platform (below), or a collaborator invite and `gh release download` | No crash and no broken agent. Each person reached `trove doctor` and a first report. `trove uninstall` restored their agent config. |
| 1. Invited beta | People the author picks: a mix of API-key and subscription users, macOS and Linux, a few heavy spenders | 10 to 15 | The same, with the tester note below | The install matrix is filled in on clean machines. Time to first report is measured. At least one `trove reconcile` against a real invoice has been analysed. The exit thresholds (below) are agreed. |
| 2. Open beta | Strangers | 30 to 50 | The README only, which needs a public repository and tap | The agreed exit criteria in `beta-checklist.md` section 6. |

## Decisions that are still open

These are the author's to make. Until they are made, the plan above does not assume an answer.

1. **Feedback channel for Stage 1.** Options: add testers as collaborators with Read access so they can open issues;
   create a small public feedback repository and change `feedback.Repo`; or have testers copy the text
   `trove feedback` shows and send it by message.
2. **When to go public.** A date, or "after Stage 1 passes its gate".
3. **Platforms in Stage 1.** macOS only, or macOS and Linux.

## Before Stage 1

- [ ] Decide the feedback channel (open decision 1) and test it with one person who is not a collaborator.
- [ ] Agree the exit thresholds in `beta-checklist.md` section 6 (they are blank on purpose).
- [ ] Run the install matrix on clean machines, using only the README, and fill in `install-verification.md`.
- [ ] Send the tester note (below) with the install steps.
- [ ] Create the results sheet (columns are in `beta-checklist.md` section 7, one row per tester, no names).
- [ ] Make sure no test rows are left in the summary receiver (`SELECT COUNT(*) FROM submissions` is 0).

## Before Stage 2 (the public step)

- [ ] Decide to go public, then run a pre-publication sweep: leftover placeholders, `docs/security-findings.md`,
  and anything personal in the tracked files or the git history.
- [ ] Make both the repository and the Homebrew tap (`usetrove/homebrew-trove`) public.
- [ ] Fill launch placeholders only with measured numbers, and run `go run ./scripts/checkclaims --publish`.
- [ ] Rerun the `privacy` CI job and re-read `PRIVACY.md` against the code.
- [ ] Confirm the receiver's rate-limit rule is deployed (Cloudflare dashboard, Security rules).

## What gets collected

- **From testers directly:** `trove feedback` text, a `trove reconcile --export` file, a `trove contribute --export`
  file, and the day-0, day-7 and day-30 answers in `beta-checklist.md` section 3.
- **From the opt-in upload (`trove contribute enable`):** only the aggregate summary described in
  [PRIVACY.md](../PRIVACY.md). Testers choose; it is never required.
- **Not collected:** prompts, code, file paths, API keys, anything outside what testers choose to send.

## Support during the beta

- Read the feedback channel daily and answer within a day.
- A bug that breaks a tester's agent or loses data stops the line: Trove must fail open.
- Fixes ship as `v0.2.x` patch releases. Installed copies do not update themselves, so tell testers by message and
  say what changed.

## Stop and pause criteria

Pause the beta if Trove breaks someone's agent, if a reconcile result is off by more than the 2% target with no
explainable cause, or if a statement in `PRIVACY.md` turns out to be wrong.

## Tester note

> Trove is a local tool that shows where your coding agent wastes tokens. It reads the session logs your agent
> already keeps (for Claude Code and Codex CLI, read-only) and stores only numbers in `~/.trove` on your machine:
> counts, sizes and hashes, never prompts, code or file paths. It changes nothing in your agent's files unless you
> approve a fix, and `trove undo` reverses it. Nothing is sent anywhere unless you choose to send it
> (`trove contribute` shows an optional summary; `--export` saves it to a file). Remove everything with
> `trove uninstall --purge-data`. Tell me where it confused you or where a number does not match what you expect.

## Stage 0 install (macOS, Apple Silicon)

Send the file `trove_0.2.0_darwin_arm64.tar.gz` from the `v0.2.0` release, and the line for it from that release's
`checksums.txt`. The tester runs:

```
shasum -a 256 trove_0.2.0_darwin_arm64.tar.gz   # must match the value you sent
tar xzf trove_0.2.0_darwin_arm64.tar.gz
xattr -d com.apple.quarantine trove             # macOS blocks unsigned files that arrived by download or AirDrop
mkdir -p ~/.local/bin && mv trove ~/.local/bin/
~/.local/bin/trove doctor
~/.local/bin/trove
```

If `trove` is not found afterwards, `~/.local/bin` is not on their `PATH`; add
`export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc`. Other platforms use the matching archive from the same release.

## Message to send with it

> Here is Trove, version 0.2.0 (checksum below). Install steps are in the attached note. Please run `trove doctor`
> and then `trove`, and tell me: how long it took from starting to your first report, whether the first number
> surprised you, and anything that confused or broke. If something breaks, run `trove feedback` and send me what it
> shows. To remove it all: `trove uninstall --purge-data`.
