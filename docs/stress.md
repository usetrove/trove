# trove bench stress

A five-layer stress suite for the optimizer engine and the gateway. It runs offline: no provider call, no spend,
no outbound network (the upstream is a local fake). It writes only to a scratch folder that it removes at the end,
and it refuses to run if that folder resolves to your real Trove data folder.

```
trove bench stress --mode smoke --strict                  # CI: fails on any failure, skipped layer or skipped check
trove bench stress --mode full --soak 4h --out ./stress-nightly
trove bench stress --layers realism --replay-dir ~/.claude/projects --out ./replay
trove bench stress --layers fuzz,faults --seed 20261108 --json
```

The headline gives the totals: `# trove bench stress: PASS. 312 checks passed, 0 failed, 0 skipped`.
Exit status is non-zero if any check fails, if any selected layer is skipped entirely (a skipped layer proves
nothing), or, with `--strict`, if any check is skipped. Each failure lists the case, the broken invariant, the
input's size and short hash (never its content), and a repro command.

## Layers

1. **realism**: seeded random sessions across prefix size, cache hit and expiry, session length, model, tool mix,
   output size and repeat and noise rates, plus named edge cases. It is the only layer that reports savings, and
   every figure is labeled *estimated*. It ranks the parameters that move the saving and marks each against a noise
   floor (the statistic after shuffling savings across sessions). It runs on the default Claude Code path, where
   the cache-breakpoint optimizer does not apply.
2. **fuzz**: hostile inputs (huge outputs, a body over the engine's size limit, binary and invalid UTF-8, very long
   lines, CRLF, ANSI floods, regex-style backtracking inputs, 10,000-deep nesting, empty and malformed requests),
   each run through the full pipeline and through every optimizer alone, plus the tool-level entry points. Sessions
   here use a configured custom agent, so cache-breakpoint is exercised.
3. **invariants**: the checkers must catch deliberately broken inputs (a changed tool id, truncated JSON, growth past
   the declared overhead, an early-prefix edit, an unknown stored blob, an invalid request that was changed), and the
   engine must be deterministic when eight goroutines share one instance.
4. **load**: concurrent engine and gateway sessions with p50 and p99 latency (measured, not asserted), a SQLite
   contention check that every row is accounted for, a goroutine and retained-heap check after load, and a soak whose
   heap trend fails above 2 MiB/min for runs of a minute or longer.
5. **faults**: upstream 500, 429, 529, connection reset, hang, partial stream, malformed SSE, bad gzip, a full store
   (the recorder refuses every row), a panicking optimizer, a cancelled context, a hook that hangs on stdin, a hook
   given garbage, an unknown hook event, and the storage cases:
   - *kill mid-write*: simulated. The partial temporary file an interrupted write leaves is created by hand, because
     the store writes to a temporary name and renames it into place. Reopening must neither serve it nor lose complete
     entries. The store is not killed mid-call.
   - *corrupted store*: real. A stored output is tampered with on disk, and a database file is overwritten in place.
   - *full disk*: simulated at the storage seam. No disk is filled. The recovery store's refusal is injected, and the
     database write failure is real: writes go through a closed handle.
   - *read-only folder*: real, through SQLite's read-only mode, which is enforced by SQLite and not by file
     permissions, so it works the same when run as root.

   Every case must fail open. The dead-MCP-server case starts the real `trove mcp` as a child process over stdio,
   kills it while a call is in flight, and checks that calls fail rather than hang, and that hooks and the gateway still work.

## Replay of local session logs

`--replay-dir` reads Claude Code session logs (JSONL) and runs the engine on requests rebuilt from them. The rules
it keeps:

- Files are opened read-only and streamed. A test checks that a replay leaves the logs byte for byte unchanged.
- Rebuilt requests live in memory for one engine call each.
- The result holds counts and byte totals per optimizer, and an estimate of tokens removed. It holds no message
  text, no file path and no session identifier. A test plants a canary string in the logs and checks it never appears
  in the output.
- A malformed line is skipped. A file that cannot be opened or streamed is counted as unreadable.
- Nothing is written to the scratch folder, and nothing is written to git.

The optimizers measured are noise normalization and duplicate collapsing (on), and the output trimmer (shadow, so it
only estimates). The logs do not contain the system prompt or tool definitions, so the requests are smaller than
real ones, and only sampled requests are used. These are estimates.

## Cost rule: visible tokens

The cost-bound invariant compares *visible* tokens, not JSON bytes. Visible text is every string in the request
except protocol markup: types, roles, IDs, model names and cache-control markers. Tokens are that text at 3.5 bytes
per token, which is an estimate, not a provider count.

This matters for the cache-breakpoint optimizer, which declares zero overhead tokens. The provider's prompt-caching
page (read 2026-11-08 at platform.claude.com) says that cache breakpoints add no cost, and that cache writes on the
5-minute TTL bill at 1.25 times the base input price, with reads at 0.1 times. The page does not say whether the
markup bytes of a marker are billed, so the cost rule does not count them, and the test that pins this is
`TestCacheBreakpointsOverheadIsZeroVisibleTokens`. The test shows that the markers change the bytes, leave the visible
text in order unchanged, and add no visible tokens. The one shape change it allows is the system prompt moving from a
string into a one-block list, which a marker needs.

## Rules this command keeps

- Layers 2 to 5 never carry savings. The savings block exists only on the realism layer, and a test checks the JSON.
- A skipped check is never counted as a pass, and a skipped layer fails the run.
- Every number is labeled *measured* or *estimated*, or is a pass or fail count.
- Realism savings are at list price on pinned models. They exclude Trove's own tool definitions, which adapters add.
  They are simulations, not a bill.
- Nothing is written outside the scratch folder, and the report holds no content, key or path.

## Continuous integration

`.github/workflows/stress.yml` runs three jobs:

- **smoke**: every push and pull request, on Ubuntu and Windows, with `--strict`. The report goes to the job summary.
- **race**: every push and pull request, on Ubuntu: `go test -race -count=1 ./internal/stress/`.
- **nightly**: every night and on demand, on Ubuntu and Windows, `--mode full --strict --soak 60m`.

Nothing is uploaded. Actions are pinned to the same commits as `ci.yml`.

## Verification status

- Smoke mode on Windows (Go 1.27.3): all layers pass, except the storage-corruption case described below.
- `go test ./internal/stress/`: passes.
- Replay run on the developer's own session logs (213 files): completes, with no unreadable files.
- Full-mode fuzz layer: run in full; results recorded in the change summary.
- Race detector: not verified locally. The race-enabled test build failed inside the cgo tool here (exit status 2).
  The race job in CI is the first real run of it.
- Linux: not run locally. The CI smoke and race jobs are the first Linux runs.

## Defects found by this suite (fixed, pending review)

**Recovery served tampered output as genuine.** `recovery.Store.Get` returned stored bytes without checking they
hash to the ID. The fix hashes every byte, compares in constant time, removes a mismatched file, and returns
`ErrNotFound`. The MCP caller returns that as an error result, so the model is told the output is unavailable. Tests
use the failing stress input as a fixture (`internal/recovery/testdata/tampered`), and they fail with the check
disabled.

**The engine transformed bodies that are not valid JSON.** `optimize.ParseRequest` accepted raw control bytes in
strings and very deep nesting, and the cache-breakpoint optimizer rewrote them. The fix validates the input with
`json.Valid`, rejects nesting past `MaxNestingDepth` (128), and forwards such bodies unchanged. The fuzz inputs are
fixtures (`internal/optimize/testdata/invalid-json`), and a sync test keeps them equal to the corpus.

Cost of the validation (measured on this machine, Go 1.27.3, Windows; warm = bytes in cache, cold = fresh copy):

| Body | Validate, warm | Validate, cold | Existing scan, warm | Depth scan, warm |
|---|---|---|---|---|
| 100 KB | 0.27 ms | 0.32 ms | 0.06 ms | 0.03 ms |
| 1 MB | 3.0 ms | 3.1 ms | 0.84 ms | 0.32 ms |
| 8 MB | 24 ms | 24 ms | 6.4 ms | 3.0 ms |

Validation is about 350 MB/s and is dominated by `json.Valid`. A request is validated on input, and again on the
output when an optimizer changed it, so a changed 1 MB request adds about 6 ms. Unchanged requests pay one
validation. That is above the 5 ms p50 budget for large bodies. Options: drop the output validation (edits are span
replacements of already-validated bytes), or write a faster validator. Not decided.

## Known gaps

- The `profile` label from the session sampler has no effect in the simulator, so it is left out of the ranking.
- Realism savings are simulated, not measured against a real bill.
- Full mode is designed for a long run. Its fuzz layer ran in full locally; the other layers at full size, and the
  60-minute soak, run in the nightly job.
