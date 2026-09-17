# Trove

Local CLI that creates handoff briefs for AI coding sessions so a fresh agent can resume work without re-explaining context.

Handoffs combine:

1. **Git checkpoint** — branch, commit, working tree, recent commits  
2. **Session context** — task, files explored, findings, decisions, next steps, rejected approaches (from an optional transcript)

Session files are written to `.trove/sessions/<id>.md`.

---

## Setup

Requirements: Node.js 18+, a Git repository.

```powershell
cd C:\Users\jacly\trove
npm install
npx tsc
```

Optional — install the `trove` command globally from this repo:

```powershell
npm link
```

If you skip `npm link`, run commands with:

```powershell
node dist/index.js <command>
```

The examples below use `trove …`. Swap in `node dist/index.js …` if needed.

### Initialize Trove in a repo

```powershell
trove init
```

This creates `.trove/` (config, current-task, decisions, sessions, etc.).

---

## Quick test: session context from a transcript

This is the main path to verify the new handoff enrichment.

### 1. Build

```powershell
npx tsc
```

### 2. Run a non-interactive handoff with the sample transcript

From the repo root:

```powershell
trove handoff -y `
  --next "Open the saved handoff and confirm Session context looks right" `
  --transcript .\fixtures\sample-transcript.md `
  --objective "Fix login redirect to /dashboard" `
  --skip-durable
```

What you should see in the terminal:

- `Session context: N file(s), N finding(s) from transcript`
- A draft preview that includes **Session context (from transcript)**
- `✓ Handoff saved` with a path like `.trove\sessions\2026-09-17T11-46-35.md`

### 3. Open the saved handoff

```powershell
Get-ChildItem .trove\sessions\*.md |
  Where-Object { $_.Name -notlike ".*" } |
  Sort-Object Name -Descending |
  Select-Object -First 1 |
  ForEach-Object { code $_.FullName }
```

Or open the path printed by the command.

### 4. Confirm these sections exist

| Section | What to expect with the sample transcript |
|--------|-------------------------------------------|
| YAML `transcript_source` | `sample-transcript.md` |
| `## Session context` → Task summary | Mentions login redirect / dashboard |
| Files and modules explored | `src/auth/login.ts`, `src/routes/app.ts` (and/or git-changed files) |
| Key findings / Decisions / Rejected | Heuristic bullets from the chat (may be imperfect) |
| `## Exact next action` | The `--next` text you passed |
| `## Git checkpoint` | Branch, commit, working tree |

### 5. Resume packet (optional)

```powershell
trove resume "login redirect"
```

This prints a budgeted brief for the next session.

---

## Interactive handoff (manual review)

```powershell
trove handoff --transcript .\fixtures\sample-transcript.md
```

You will be asked for:

1. Next concrete action  
2. Anything Trove missed (optional)  
3. `[S] Save` / `[D] Add decision` / `[E] Edit full draft` / `[C] Cancel`

Use this when you want to confirm or edit before writing the file.

---

## Using a real Cursor chat transcript

Cursor agent transcripts are JSONL files under your project’s agent-transcripts folder, for example:

```text
C:\Users\jacly\.cursor\projects\c-Users-jacly-trove\agent-transcripts\<uuid>\<uuid>.jsonl
```

List recent ones:

```powershell
Get-ChildItem "$env:USERPROFILE\.cursor\projects\c-Users-jacly-trove\agent-transcripts" -Recurse -Filter *.jsonl |
  Where-Object { $_.FullName -notmatch '\\subagents\\' } |
  Sort-Object LastWriteTime -Descending |
  Select-Object -First 5 FullName, LastWriteTime
```

Then:

```powershell
trove handoff -y `
  --next "Continue from this chat" `
  --transcript "C:\Users\jacly\.cursor\projects\c-Users-jacly-trove\agent-transcripts\<uuid>\<uuid>.jsonl" `
  --skip-durable
```

You can also set an env var instead of `--transcript`:

```powershell
$env:TROVE_TRANSCRIPT = "C:\path\to\chat.jsonl"
trove handoff -y --next "Continue" --skip-durable
```

---

## Baseline test (no transcript)

Still useful to confirm Git-only handoffs work:

```powershell
trove handoff -y --next "Confirm Git checkpoint still renders" --skip-durable
```

The saved file should still have `## Git checkpoint`.  
`## Session context` should say no transcript was provided.

---

## CLI reference

| Command | Purpose |
|--------|---------|
| `trove init` | Create `.trove/` in the current Git repo |
| `trove handoff` | Draft + save a session handoff |
| `trove resume [query]` | Print a budgeted resume packet |

### `trove handoff` options

| Flag | Meaning |
|------|---------|
| `[next]` / `--next <text>` | Exact next action (required with `-y`) |
| `-y, --yes` | Non-interactive save |
| `--transcript <path>` | Chat transcript (`.jsonl` or markdown) |
| `--objective <text>` | Override detected objective |
| `--decisions <text>` | Propose a durable decision |
| `--rejected <text>` | Propose a durable rejected approach |
| `--skip-durable` | Skip durable memory prompts |

Supported transcript formats:

- **Cursor JSONL** — one JSON object per line with `role` + `message.content[]`
- **Markdown chat** — `## User` / `## Assistant` sections (see `fixtures/sample-transcript.md`)

---

## Troubleshooting

**`Trove has not been initialized`**  
Run `trove init` in the repo root.

**`Transcript not found`**  
Check the path. Use an absolute path if relative resolution is unclear.

**`Session context: transcript provided but no messages parsed`**  
File exists but wasn’t recognized. Prefer Cursor `.jsonl`, or markdown with `## User` / `## Assistant` headings.

**Draft looks generic / noisy**  
V1 summarization is heuristic (no LLM). Prefer a short focused transcript, then fix details in interactive review (`[E]` edit) or the “anything missed?” prompt.

**`trove` not found**  
Run `npx tsc` then `npm link`, or use `node dist/index.js …`.

---

## Project layout (relevant bits)

```text
src/
  commands/handoff.ts      # CLI orchestration
  handoff/
    collectTranscript.ts   # load + parse transcript
    summarizeSession.ts    # heuristic session context
    collectEvidence.ts     # git + memory + transcript
    autoDraft.ts           # draft builder
  templates/handoff.ts     # markdown renderer
fixtures/
  sample-transcript.md     # tiny chat for manual testing
.trove/
  sessions/                # saved handoffs
  current-task.md          # pointer to latest handoff
```
