# Decision: sharing duplicate in-flight requests (plan item 4.4)

Status: DECIDED 2026-11-17. **Do not build sharing now.** Keep `inflight_dedup` as shadow only.

## The question

Should the gateway make one provider call for identical requests that arrive while the first is still waiting for the
provider, and give every client that answer? Probe0's materials claim about 7% savings from cache timing plus
parallel-request handling. Trove's own data says much less: on one real history, holding sibling requests until the
first cache entry existed would have saved a small fraction of one percent of spend (docs/techniques.md, C2, verified
2026-11-14). Probe0's 7% is not comparable: it mixes cache timing (which C2 and `cache_breakpoints` already cover) with
true duplicates, and its method is not published.

## The bar

Sharing gets built only if the shadow estimate is **more than 1% of gateway spend** over a period, on real API-key
traffic, together with the other exit criteria in docs/inflight-dedup.md (known cause by agent, decided opt-in wording,
the listed tests). At or below 1%, the risks there (silently merging samples a framework wanted separately, stream
replay, leader disconnects, shared errors) are not worth taking.

## How the bar is checked

`trove report` puts the shadow estimate against the bar on the `inflight_dedup` row: `spend_share_pct` in `--json`
(estimated) and a note saying whether it is above or at/below 1% (`report.InflightDedupBar`,
`TestReportStatesInflightDedupShareAgainstTheOnePercentBar`). The share is of gateway spend only, because history
traffic never passes the gateway and so can never be shared.

## Evidence today

No real API-key install has run `inflight_dedup = "shadow"` long enough to report a share. The only real figure (C2
above) is well under 1%. So the decision is no. Revisit when a report shows the row above the bar across several
sessions; until then nothing more is built.
