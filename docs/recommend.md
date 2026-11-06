# The local recommender and per-repo tuning

`trove recommend` and the Recommendations section of `trove report` suggest which
optimizations and settings profile to try for a repo and task, each with a reason
and a confidence level. `trove tune` tunes the output trimmer for one repo from
that repo's own results. All of it runs on your device, makes no network call and
**changes nothing by itself**.

Code: `internal/recommend` (rules and lookup table), `internal/tuning` (per-repo
values, review), `internal/features` ([features.md](features.md)).

## What it looks at

- the derived features for the repo and its recent sessions (numbers only);
- your own A/B results (`trove experiment status`, internal/experiment);
- optimizer states from `config.toml`, and any guardrail rollback;
- the **bundled defaults file**, `internal/recommend/defaults.toml`.

It never sees prompts, code, paths or tool output.

## What it suggests

| Action | Meaning | Where it comes from |
|---|---|---|
| `shadow` | set an optimizer to shadow mode: it estimates and changes nothing | a rule in the defaults file |
| `enable` | set an optimizer to on | **only** your own measured A/B result saying it saves, with outcomes no worse |
| `avoid` | leave it off | your own A/B result: it cost more, outcomes got worse, or a guardrail rolled it back |
| `use_profile` | apply a settings profile with `trove settings` | the task type you gave with `--task` |
| `tune` | use a tuned parameter value in this repo | `trove tune` (below) |

The defaults file can only ever say `shadow` or `use_profile`; the loader rejects an
`enable` rule (CLAUDE.md rule 6). An optimizer that a guardrail rolled back, or that
your own result says hurts, is never suggested.

### Confidence

A plain label, not a probability.

| Level | When |
|---|---|
| high | your own A/B result with a narrow range and many sessions, or 100+ trimmed outputs behind a tuning proposal |
| medium | your own A/B result labelled medium; a rule backed by a benchmark run (`evidence = "benchmark"`) **and** at least 5 recorded sessions in the repo; or 50+ trimmed outputs behind a tuning proposal |
| low | everything else, including **every rule in the defaults file today** |

### Labels

Each suggestion says what its figures rest on: "measured" for numbers from your own
A/B results or the hooks' counts, "estimated" for anything derived from features or
rules. A rule of thumb shows no dollar figure, because none exists.

## The bundled defaults file

`defaults.toml` is a lookup table: *when the repo and its sessions look like this,
suggest this*. It is versioned two ways:

- `version`: the schema version. A Trove that reads up to version N rejects a newer file
  with a message to update.
- `release`: the name of the release or benchmark bundle the rules came from. Reports
  show it ("defaults seed-1") so you can tell which rules produced a suggestion.

Every rule carries `evidence`:

- `"none"`: a rule of thumb. **All rules in the current file are this**, because the
  benchmark lab has produced no results yet (`bench/results` holds only smoke runs).
- `"benchmark"`: backed by a reproducible bench run; `source` must name the run.

### Updating it with each release

1. Run the benchmark lab and keep its results (`bench/`).
2. For each rule the results support, set `evidence = "benchmark"` and `source` to
   the run's id; add rules for newly proven optimizers; remove rules the results
   contradict.
3. Bump `release`. Bump `version` only if the schema changes (and teach `LoadDefaults`
   the new shape).
4. `go test ./internal/recommend`. The tests load the file, check the schema rules and
   check that every rule names an optimizer an adapter implements or a settings
   profile that exists.

`internal/experiment/priors.toml` (benchmark priors used to shrink your A/B estimates)
is a separate file with its own version; a benchmark run should update both.

## `trove recommend`

```
trove recommend [--repo DIR] [--task quick-fix|feature|refactor|exploration] [--json]
```

Defaults to the current folder. With no recorded sessions it says so; with `--task`
it still suggests that task's settings profile. `trove report` shows the same
suggestions (for the current folder) in its Recommendations section, after the
usage-based ones, each with its reason, confidence, evidence and "No savings figure."
for rules of thumb.

## Per-repo tuning (`trove tune`)

The output trimmer's thresholds are tunable per repo: `min_bytes`, `min_lines`,
`min_trimmed_lines`, `keep_head`, `keep_tail`, each with a hard range
(`optimize.TrimParams`) that every value is clamped to when it is read.

**The signal** is the share of trimmed outputs your agent fetched back in this repo
(measured, from the hooks' counts). Trove already rolls the trimmer back everywhere
at `refetch_rate_percent` (default 25%). Per repo it acts at half that: with at least
`min_sessions` trimmed outputs behind it, it proposes keeping more of each output
(`min_bytes` 6000 to 9000, `keep_head` 5 to 7, `keep_tail` 10 to 15).

```
trove tune             # compute and show suggestions; nothing changes
trove tune apply       # put them in effect for NEW sessions in this repo
trove tune reset       # forget this repo's tuning
```

- It only proposes while the trimmer is **on**, never while a tuning is applied, and
  not again after one was reverted (`reset` allows a new try).
- **Session boundary (rule 5).** Applied values reach the hooks through their
  snapshot and are pinned with the optimizer states when a session starts. A running
  session keeps what it started with.
- **Automatic revert (rule 6).** At session end and on `trove report`, Trove compares
  the fetch-back share since the values were applied with the share before. Once at
  least `min_sessions` outputs have been trimmed since, it goes back to the defaults
  if the share did not fall, or if it reached the guardrail limit.
- Tuned values stay on this device (table `repo_tuning`: repo hash, names, numbers).
  Sharing them as metadata under `trove contribute` is **not built yet**.

## Hosted settings bundle (not built)

`recommend.Provider` is the seam. `recommend.Compose` runs providers in priority order;
the earlier one wins on the same kind and name, and a provider that fails is skipped.
A future opt-in hosted provider would be listed first. Nothing here calls the network,
and adding a provider that does needs a new rule in CLAUDE.md (rule 1).

## Token overhead (rule 15)

None. The recommender and tuning inject no text into any request and add no tool
definitions.

## Verification log (rule 8)

No provider or agent detail is used beyond what [features.md](features.md) lists. The
suggested optimizers and profiles are Trove's own; the settings profiles are verified
in [settings.md](settings.md).

## Known gaps

- No rule is benchmark-backed, so every rule-based suggestion is low confidence.
- No rule for scout delegation: the feature is collected, but there is no basis yet
  for recommending a scout setup.
- A/B results are not per repo (the experiment stratum is empty today), so "saves",
  "avoid" and rollbacks apply to the whole device.
- Tuning only loosens the trimmer. Tightening would need a size histogram of tool
  results, which is not collected.
- The gateway and the OpenCode adapter get no tuning: neither knows which repo a
  request is for.
- Applying and reverting tuning takes effect for new sessions only, by design.
