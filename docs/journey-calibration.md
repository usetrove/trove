# Calibration of the journey simulation

The offline journey tier (`trove bench journeys --tier offline`) generates synthetic agent
sessions. Before any savings are shown, the generator's tool output has to look like real
Claude Code output. This page says what that check compares, against what, and where it is weak.

## The source is one developer's sessions

The only real data is **one developer's Claude Code session logs**: 210 session files on one
Windows machine, about 13,000 tool results, from one person's work. It is not a sample of users,
of projects, of models, or of operating systems. A pass means the generator reproduces that
developer's tool output. It does not mean it reproduces yours.

Only aggregates are kept and committed (`internal/journey/calibration_targets.json`): counts,
median and 90th-percentile sizes in bytes, the share of outputs with color codes, and the share
of lines that repeat an earlier line in the same output. No text, path, prompt or session ID is
read into the committed file or printed.

## What is compared

Per tool class (Read, Grep, Bash):

| Measure | How it is computed |
|---|---|
| Size | Median and 90th percentile of output length in bytes |
| Color codes | Share of outputs containing an ESC `[` sequence |
| Repeated lines | Lines that equal an earlier line in the same output, as a share of all lines |

Each simulated tool class draws from the generator's output kinds with fixed weights
(`calibrationTools` in `internal/journey/calibrate.go`). Read is `read_small` and `read_large`
at 2:1. Grep is `grep`. Bash is `bash_small`, `build_log`, `test_verbose` and `stack_trace` at
12:1:1:2.

## Split, so the gate is not circular

Session files are sorted by path. Even positions are the **fit** half, which sets the generator's
parameters. Odd positions are the **holdout** half, which the gate checks. A generator tuned to
every file would pass by construction.

Known limit: while tuning, Grep's 90th percentile differed between the halves (3,794 B fit,
5,039 B holdout, from 454 and 329 results). Grep's tail is the least stable measure here. I set
the Grep tail to 4,400 B, between the two, so the holdout is **not fully independent for Grep**.
Other tool classes were not adjusted to the holdout.

## Tolerance

- Sizes (median and 90th percentile): within a factor of 1.25 of the holdout target, either way.
- Shares (color codes and repeated lines): within 5 percentage points.

The gate runs on a fixed seed (`calibrationSeed`), with 1,500 generated outputs per tool class.
Its verdict is printed at the top of every report. When it fails, the report prints no savings
table, and the JSON carries the same status.

## What is fitted

- **Sizes:** each output kind is drawn from a log-normal with a fitted median and 90th percentile
  (`kindSize` in `internal/journey/gen.go`).
- **Mixture weights:** the per-tool weights above.
- **Blank lines:** command output repeats blank lines at 18% per line (`blankShare`).
- **Read output:** every line gets a line number (`cat -n` style, `numberLines`). Real Read output
  has no repeated lines, which is consistent with line numbering, but I did not inspect a raw
  Read result to confirm the format; this is inferred from the 0% repeated-line share.

## What is not modeled

- Real Read output is not byte-identical to the generator's. Line-number width and the handling
  of trailing whitespace are approximations.
- Subagent output is generated with the same shapes as parent output.
- Color codes are absent from the generator's tool output, matching the logs (0.2% of Bash
  outputs). Noise removal therefore has little to remove in these sessions. If your own tool
  output has more color codes, the simulation understates that optimizer.
- Tokens are bytes divided by a fixed ratio. The calibration says nothing about the tokenizer.

## Reproducing the check

Regenerate the aggregates from any folder of Claude Code session logs (`*.jsonl`):

```
trove bench journeys --calibrate <folder> --out <dir>
```

This writes `calibration_targets.json` and `calibration.md` into `<dir>`. It prints the gate for
that folder. Only the committed `internal/journey/calibration_targets.json` is used by the
journey report, so the gate gives the same answer on every machine, including CI. To change the
committed targets, copy a regenerated file over it in a reviewed commit, and say which folder it
came from.
