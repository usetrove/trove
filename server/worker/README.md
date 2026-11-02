# Summary receiver

A small Cloudflare Worker with a D1 database that receives the optional anonymous
summary from `trove contribute enable`. It is here so you can read exactly what the
server does with what it receives.

- `POST /v1/contribute` validates the body against a fixed list of fields
  (`validate.js`, mirroring `internal/contribute/payload.go`) and stores the JSON plus
  the UTC day it arrived. Anything unknown is rejected.
- `DELETE /v1/contribute/<install id>` removes every row stored under that id.
- It never reads or stores the IP address, headers or cookies, and never logs a
  request or a body (a test fails if `index.js` mentions the client address or logs).
  Workers Logs is switched off in `wrangler.toml` because Cloudflare turns it on by default
  for new Workers.
- It stores at most 5,000 summaries per UTC day and answers 429 after that, which bounds
  storage if someone floods it with made-up install ids. Add a Cloudflare rate-limiting
  rule in the dashboard if you want a per-address limit; the Worker itself never sees addresses.
- A daily cron deletes rows older than 365 days.

## Deploy

```
cd server/worker
cp wrangler.toml wrangler.local.toml             # gitignored: keep your own ids out of the repo
npx wrangler d1 create trove-contribute          # copy the database id into wrangler.local.toml
npx wrangler d1 execute trove-contribute --remote --file=schema.sql
npx wrangler deploy --config wrangler.local.toml
```

`wrangler.toml` is the template and keeps a placeholder id. Put your real database id only in
`wrangler.local.toml`, which git ignores, and pass `--config wrangler.local.toml` to every wrangler
command that needs the binding (`deploy`, `tail`, `d1 execute` by name works without it).

Attach your own domain with a `[[routes]]` entry (`custom_domain = true`) in `wrangler.local.toml`, then set
`endpoint = "https://<your domain>/v1/contribute"` under `[contribute]` in `config.toml`. Trove's built-in default
points at the project's own receiver (`DefaultContributeEndpoint` in `internal/config/config.go`). Do not enable
Cloudflare request logging, Logpush or Web Analytics for this worker: the "no IP address
stored" statement in PRIVACY.md depends on it.

## Per-address rate limit (recommended, set in the dashboard)

The Worker never sees client addresses, so a per-address limit belongs at the edge, in
front of it. In the Cloudflare dashboard open **Security rules**, then **Create rule**,
then **Rate limiting rules**, and set:

- **Rule name:** `trove-contribute flood limit`
- **If incoming requests match:** Field **URI Path**, operator **equals**, value
  `/v1/contribute`. (Some plans hide the Hostname field. If the builder has an
  **Edit expression** link you can use
  `(http.host eq "contribute.troverelay.com" and http.request.uri.path eq "/v1/contribute")`
  instead, with your own hostname.)
- **With the same characteristics:** IP.
- **When rate exceeds:** a low number, such as 10 requests, over the longest period the
  plan offers. A real install sends one request a week, so even a shared office address
  stays far below this.
- **Then take action:** Block, with the longest **Duration** the plan offers.

The daily cap in `index.js` stays as the backstop for anything this rule misses. Check the
plan limits in the dashboard (rule count, periods, durations) because they depend on your plan.

## Test

```
cd server/worker && node --test
```

The test also reads `internal/contribute/testdata/payload.golden.json`, so a change to the
Go payload that the validator would reject fails here. When you add a payload field, change
`payload.go`, `validate.js`, the field list in `payload_test.go`, `SchemaVersion` and
PRIVACY.md together.
