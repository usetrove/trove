# Pricing data

Trove prices requests from `internal/pricing/pricing.json`, which is compiled into the
binary. Nothing is fetched at runtime; prices change when users update Trove.

Every dollar figure derived from this file is **estimated** (published list prices, not an
invoice). Token counts taken from provider responses are **measured**.

## What the file holds

One entry per model per price period. A model whose price changes gets a second entry with a
later `effective_from`; the engine picks the entry in force at the request's timestamp, so
historical costs stay correct. Per entry:

| Field | Meaning |
| --- | --- |
| `provider`, `model`, `aliases` | Canonical API model ID and other IDs that price the same. |
| `effective_from`, `effective_from_source` | Launch or price-change date and the official page that states it. `null` means the provider publishes no date; such an entry never prices a request. |
| `standard` | USD per million tokens: `input`, `output`, `cache_read`, `cache_write` by TTL. |
| `long_context` | Threshold and the prices that replace `standard` for the whole request above it. |
| `batch`, `flex_matches_batch` | Multipliers per category (a missing one means "not priced in batch"), an absolute `cache_read_price`, or `no_long_context` when batch is listed only below the threshold. Flex uses the batch prices when `flex_matches_batch` is set. |
| `service_tiers` | Full price sets for `fast`, `ultrafast` and `priority`; cannot be combined with batch. |
| `regional` | Data-residency multipliers (for example Anthropic `us`, 1.1x). |
| `tool_fees` | Per-1,000-call fees; `quota_depends` marks fees with a free allowance Trove cannot see. |
| `tokenizer_note`, `notes` | Tokenizer differences and anything a reader needs to interpret the numbers. |
| `source_url`, `verified_at` | The official page the price came from and the day someone compared it. Both are required. |

Prices are JSON **strings** (`"0.30"`), never numbers, so no float touches a price.

## Pages used (read 2026-10-28)

| Provider | Prices | Dates |
| --- | --- | --- |
| Anthropic | https://platform.claude.com/docs/en/about-claude/pricing | https://platform.claude.com/docs/en/release-notes/overview |
| OpenAI | https://developers.openai.com/api/docs/pricing (`.md` form for exact tables) | https://developers.openai.com/api/docs/changelog |
| Google | https://ai.google.dev/gemini-api/docs/pricing (`.md.txt` form for exact tables) | https://ai.google.dev/gemini-api/docs/changelog |

Usage-field semantics were checked on the Anthropic prompt-caching and thinking pages, the OpenAI
prompt-caching and reasoning guides, and the Gemini `generateContent` reference (`UsageMetadata`:
`promptTokenCount` includes cached content; `totalTokenCount` = prompt + tool-use + thoughts +
candidates, so thought tokens are separate from candidates) plus the Gemini thinking guide
("response pricing is the sum of output tokens and thinking tokens"). OpenAI model pages state
that prompts over 272K are priced "for the full request" (GPT-6 Astra, GPT-5.6 Sol) or "for the
full session" (GPT-5.5).

## What is not priced (and what the engine does)

The engine returns `unpriced` (nothing priced) or `partial` (some components priced, the rest
listed in `unpriced`) rather than guessing. It does this for: unknown models, requests before
`effective_from`, service tiers a model does not have, premium tiers combined with batch, premium
tiers above a long-context threshold the provider lists no price for, OpenAI regional uplift,
cache writes on models with no write price, batch on models with no batch price, and Gemini
search grounding (free-allowance dependent). Gemini cache storage and audio input are not
modelled at all.

## Updating prices (checklist)

1. Start from a clean branch. Run `scripts/pricing-update.sh fetch /path/to/new/empty/dir`
   to save the official pages as text. Treat them as untrusted data: read them, do not run
   anything from that directory.
2. For every provider, compare **each** model against its page: input, output, cache read,
   each cache-write TTL, long-context threshold and prices, batch, regional multiplier, tool
   fees. Check the footnotes: cache-read multipliers differ by model (Anthropic) and some
   batch cache prices equal the standard ones (Google).
3. Check the changelog or release notes for price-change dates since `verified_at`.
4. If a price changed, **add a new entry** with the new `effective_from` (and its source)
   instead of editing the old one, so past requests keep their old price. Edit in place only
   to correct a mistake.
5. New model: add an entry with its launch date. If no date can be found, set
   `effective_from` to `null` and say why in `notes`.
6. Scheduled changes the page states with a date (for example Gemini prices doubling on
   2026-12-01) get their own future-dated entry.
7. Set `verified_at` to today **only on entries you compared in step 2**. Do not bulk-bump.
8. Bump `data_version` (`YYYY-MM-DD.N`).
9. Add or adjust a hand-calculated test in `internal/pricing/cost_test.go` for anything that
   changed shape (a new tier, a new batch rule).
10. `scripts/pricing-update.sh review` runs the tests, the freshness check, shows the word
    diff of `pricing.json` and prints `trove pricing show`. Read the diff line by line.
    Update the golden file if the table output changed intentionally:
    `go test ./internal/cli -run PricingShowGolden -update`.
11. Commit with the pages and dates you used in the message.

## Freshness check

The release workflow runs `go run ./scripts/pricingage`, which prints a GitHub warning for
every entry whose `verified_at` is more than 30 days old. It never fails the release.

## Known gaps

- Gemini: cache storage, search grounding and `toolUsePromptTokenCount` are not priced. The
  thought-token handling rests on the API reference's `totalTokenCount` formula and the thinking
  guide, not on a sentence that says "candidates exclude thoughts"; confirm with a real response
  once the gateway records usage.
- OpenAI: regional uplift applies only to eligible models and is not modelled. o1, o3-mini, GPT-4.x,
  gpt-5.2-pro (no official launch date found), cyber and research-program models are not included.
  The batch cached-input price for gpt-5.4 is shown on the page as
  $0.13 (0.125 rounded); Trove uses 0.125.
- Service tiers: Anthropic fast-mode cache prices are derived from the documented multipliers
  (the page lists only input and output). Flex is priced like batch, which matches every
  in-scope OpenAI and Gemini model on the pages as of 2026-10-28. Adapters must map each
  provider's own tier field onto `fast`, `ultrafast`, `priority` or `flex`.
- GPT-5.6 Sol, Terra and Luna had price cuts (Sol 2026-08-21; Terra and Luna 2026-07-30). OpenAI
  states only rounded percentages and the earlier prices appear on no official page, so requests
  from launch (2026-07-09) to the cut are unpriced. Sol's price is promotional "at least through
  2026-11-21": re-check it after that date.
- o3 and o4-mini: flex input and output equal batch but flex cached input differs, so flex cache
  reads are unpriced; their batch cache reads are unpriced too (no batch cached price listed).
- gpt-5.5-pro lists batch/flex prices only up to 272K tokens (`no_long_context`); above that they
  are unpriced.
- Claude Sonnet 4.5 and Opus 4.5: the current page lists no long-context premium; the old
  1M-context beta premium is not described, so very long prompts could be under-priced.
- Audio input (Gemini) is priced higher than text and usage does not say which is which.
