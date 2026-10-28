# Pricing verification checklist

Written 2026-10-28 against pricing data version `2026-10-28.1` (commits 7a800c5, 1e9f7bf, 80835d1).
Work through each item, tick it, and write the outcome next to it. If an item changes the data,
follow the update checklist in `docs/pricing.md`.

To print the data, run this from the repo root (the `trove` on your PATH is an old build
without the pricing command):

    go run ./cmd/trove pricing show            # table
    go run ./cmd/trove pricing show --json     # full detail, incl. batch, long context, sources

Or rebuild the installed one: `go install ./cmd/trove`.

## Before relying on any number

- [x] **1. Spot-check rows against the live pages.** At least one model per provider, comparing
      input, output, cache read, cache write, batch, long-context.
      Pages: https://platform.claude.com/docs/en/about-claude/pricing,
      https://developers.openai.com/api/docs/pricing,
      https://ai.google.dev/gemini-api/docs/pricing.
      Extra attention: Anthropic cache-read prices (they differ by model).
      Outcome (Claude, 2026-10-28): DONE for prices. A scratch test parsed the raw official tables and compared 719 prices (standard, batch, flex, fast, ultrafast, priority, long-context, cache writes) with what the engine returns: 718 match, 1 differs by display rounding (gpt-5.4 batch/flex cached input: page shows $0.13, Trove uses 0.125). NOT covered by this check: effective_from dates (item 3), Anthropic fast-mode cache prices (derived), scheduled future-dated Gemini entries, tool fees, regional multipliers. Worth a quick human glance at those.
- [x] **2. Opus 5.5 cache-read price.** Recorded as $0.20 since launch (2026-09-22). Sonnet 5.5
      dropped on 2026-10-07; confirm Opus 5.5 did not change too.
      Check: https://platform.claude.com/docs/en/release-notes/overview
      Outcome (Claude, 2026-10-28): CONFIRMED. The Opus 5.5 model page lists cache read $0.20 and the pricing footnote says 0.05x base input; the 2026-10-07 release note names only Sonnet 5.5, and the 2026-09-22 launch entry lists no cache-read price change.
- [ ] **3. `effective_from` dates.** They come from changelogs, not the pricing pages. Check a
      few, especially Gemini 3.1 Flash-Lite and 2.5 Flash-Lite (GA date used; preview prices not
      recorded) and the Gemini preview models.
      Outcome: ______

## Assumptions I could not confirm

- [x] **4. Gemini thought tokens.** Is `thoughtsTokenCount` already inside the reported output
      count? Until confirmed, the engine flags thought tokens as unpriced instead of adding them.
      Check the Gemini API `usageMetadata` reference. If separate, make the engine add them to
      output for Google.
      Outcome (Claude, 2026-10-28): RESOLVED from docs, engine changed. The generateContent reference defines totalTokenCount = prompt + tool-use + thoughts + candidates (so thoughts are separate from candidates) and the thinking guide says response pricing is the sum of output and thinking tokens. The engine now bills Reasoning on top of Output at the output price for Google. Remaining caveat: no doc sentence says "candidates exclude thoughts" outright; confirm with a real response once the gateway records usage. promptTokenCount includes cached content (stated).
- [x] **5. OpenAI long context (over 272K).** Assumed the higher rate covers the whole request.
      Look for an explicit statement in OpenAI's docs. If it applies only above the threshold,
      long-context costs are overstated and `long_context` handling must change.
      Outcome (Claude, 2026-10-28): CONFIRMED whole request. Model pages: gpt-6-astra "priced at 2x input and cache rates and 1.5x output for the full request"; gpt-5.6-sol "...for the full request"; gpt-5.5 "...for the full session". Checked three models; the rest of the family is assumed to follow.
- [ ] **6. GPT-5.6 Sol/Terra/Luna earlier prices.** Cuts on 2026-08-21 (Sol) and 2026-07-30
      (Terra, Luna); earlier prices are not on any page I read, so older requests are unpriced.
      Decide whether that is acceptable or whether to find the old prices from official sources.
      Also: Sol's price is promotional "at least through 2026-11-21", so re-check after that date.
      Outcome: ______

## Things not run

- [ ] **7. CI.** Nothing is pushed. After pushing, confirm the lint job passes (my full-repo
      lint run timed out locally; the packages I touched were clean) and that the release
      workflow's "Check pricing freshness" step (`go run ./scripts/pricingage`) runs.
      Outcome: ______
- [ ] **8. `scripts/pricing-update.sh`.** Never run, and shellcheck is not installed here. Try
      `scripts/pricing-update.sh fetch <new empty dir>` and `scripts/pricing-update.sh review`;
      confirm the `.md` URLs still work. Run shellcheck on it if you add it to CI.
      Outcome: ______
- [ ] **9. Race tests.** CI runs `go test -race ./...` (needs cgo). Not run locally.
      Outcome: ______

## Product decisions

- [ ] **10. Unpriced families.** o3/o4-mini, pro/cyber variants, Mythos, older models currently
      return "unpriced". Add any you need.
      Outcome: ______
- [x] **11. Fast / priority / flex / ultrafast tiers.** Return unpriced today. If the gateway
      will see them, add a service-tier field wired to the right prices (the OpenAI and Google
      pages list them; Anthropic fast mode for Opus is in its pricing page).
      Outcome (Claude, 2026-10-28): IMPLEMENTED. Request.ServiceTier now accepts fast (OpenAI, Anthropic Opus 5.5/5/4.8), ultrafast (OpenAI astra, 6.1-sol), priority (Gemini) and flex (OpenAI, Gemini; priced like batch). Premium tiers cannot combine with batch; fast on models with a long-context threshold but no listed fast long-context price is unpriced above the threshold. Anthropic fast cache prices are derived from the documented multipliers. Gateway adapters still need to map each provider's own tier field onto these names.

## Also known (from docs/pricing.md "Known gaps")

- Sonnet 4.5 / Opus 4.5: no long-context premium on the current page; old 1M-beta premium
  not described, so very long prompts could be under-priced.
- OpenAI regional (data-residency) uplift not modelled; Gemini cache storage, search grounding
  and audio input not priced.
- Re-run the freshness check (`go run ./scripts/pricingage`) after 2026-11-27, when entries
  turn 30 days old.
