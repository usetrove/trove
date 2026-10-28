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
- [x] **3. `effective_from` dates.** They come from changelogs, not the pricing pages. Check a
      few, especially Gemini 3.1 Flash-Lite and 2.5 Flash-Lite (GA date used; preview prices not
      recorded) and the Gemini preview models.
      Outcome (Claude, 2026-10-28): VERIFIED against the raw (not summarized) release notes and changelogs. Anthropic: all 14 launch dates and the 2026-10-07 Sonnet 5.5 change match the date headings. OpenAI: every model date matches a "Released ..." changelog entry; the three GPT-5.6 price-cut dates match. Google: Argon 10-13, 3.8 Flash 09-02, 3.6 Flash and 3.5 Flash-Lite 07-21, 3.1 Flash-Lite GA 05-07, 3.1 Pro Preview 02-19, 3 Flash Preview 2025-12-17, 2.5 Pro and Flash 2025-06-17, 2.5 Flash-Lite 2025-07-22 all match. Remaining judgment calls (not errors): Gemini preview models are dated at the preview launch; Gemini 3.1 Flash-Lite and 2.5 Flash-Lite at GA; and for older models (GPT-5, 5.1, 5.2, Gemini 2.5) I scanned the changelogs for price-change statements and found none, which does not prove none happened.

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
- [x] **6. GPT-5.6 Sol/Terra/Luna earlier prices.** Cuts on 2026-08-21 (Sol) and 2026-07-30
      (Terra, Luna); earlier prices are not on any page I read, so older requests are unpriced.
      Decide whether that is acceptable or whether to find the old prices from official sources.
      Also: Sol's price is promotional "at least through 2026-11-21", so re-check after that date.
      Outcome (Claude, 2026-10-28): NOT RECOVERABLE from official sources, so left unpriced on purpose. The changelog gives only rounded percentages (Sol "20% lower input, 33% lower output"; Terra "20% less"; Luna "80% less"); the model pages, the GPT-5.6 guide and the pricing page show only current prices. Back-calculating (Sol $5/$30, Terra $2.50/$15, Luna $1/$6) would be a guess, and cached-input and cache-write prices cannot be derived at all. Requests from launch (2026-07-09) to the cut are unpriced. STILL TO DO: re-check Sol after 2026-11-21 (promotional pricing "at least through" that date). If you want estimates for that window anyway, say so and label them estimated-from-percentages.

## Things not run

- [x] **7. CI.** Nothing is pushed. After pushing, confirm the lint job passes (my full-repo
      lint run timed out locally; the packages I touched were clean) and that the release
      workflow's "Check pricing freshness" step (`go run ./scripts/pricingage`) runs.
      Outcome (2026-10-28): CI run 40776834158 on 7bca173 passed all 10 jobs, including full-repo lint, vulncheck and the four platform builds. Still unexercised on GitHub: the release workflow's "Check pricing freshness" step, which only runs on a v* tag (the same command, go run ./scripts/pricingage, ran fine locally).
- [x] **8. `scripts/pricing-update.sh`.** Never run, and shellcheck is not installed here. Try
      `scripts/pricing-update.sh fetch <new empty dir>` and `scripts/pricing-update.sh review`;
      confirm the `.md` URLs still work. Run shellcheck on it if you add it to CI.
      Outcome (Claude, 2026-10-28): RAN IT and found a bug: the Anthropic URLs and the Google changelog URL returned HTML (1.3 MB each), not readable text. Fixed by using the .md / .md.txt endpoints; all six files now download as readable text, the non-empty-directory guard works, and `review` runs the tests, the freshness check, the diff and `trove pricing show`. shellcheck is not installed here, so only `sh -n` (syntax) was checked; run shellcheck if you add the script to CI.
- [x] **9. Race tests.** CI runs `go test -race ./...` (needs cgo). Not run locally.
      Outcome (2026-10-28): passed in CI (go test -race ./... on ubuntu-latest and macos-latest) for 7bca173.

## Product decisions

- [x] **10. Unpriced families.** o3/o4-mini, pro/cyber variants, Mythos, older models currently
      return "unpriced". Add any you need.
      Outcome (Claude, 2026-10-28): ADDED gpt-5.5-pro, gpt-5.4-pro, gpt-5-pro, o3-pro, o3 (from the 2025-06-10 price cut), o4-mini, claude-mythos-5 and claude-mythos-5-1, each cross-checked against the pages. NOT ADDED: gpt-5.2-pro (no official launch date found), o1 and o3-mini (older), GPT-4.x family, gpt-5.6-cyber and gpt-5.5-cyber (separate approval program), gpt-rosalind (research program), Gemini 3.7 Flash (launched 2026-08-13 per the changelog but not on the pricing page, so no verifiable price). Say which of these you want and I will add them.
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
