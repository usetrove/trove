# Summary receiver

A small Cloudflare Worker with a D1 database that receives the optional anonymous
summary from `trove contribute enable`. It is here so you can read exactly what the
server does with what it receives.

- `POST /v1/contribute` validates the body against a fixed list of fields
  (`validate.js`, mirroring `internal/contribute/payload.go`) and stores the JSON plus
  the UTC day it arrived. Anything unknown is rejected.
- `DELETE /v1/contribute/<install id>` removes every row stored under that id.
- It never reads or stores the IP address, headers or cookies, and never logs a
  request or a body. A daily cron deletes rows older than 365 days.

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

Then set `endpoint = "https://<your worker>/v1/contribute"` under `[contribute]` in
`config.toml` (or make that URL the built-in default in a release). Do not enable
Cloudflare request logging, Logpush or Web Analytics for this worker: the "no IP address
stored" statement in PRIVACY.md depends on it.

## Test

```
cd server/worker && node --test
```

The test also reads `internal/contribute/testdata/payload.golden.json`, so a change to the
Go payload that the validator would reject fails here. When you add a payload field, change
`payload.go`, `validate.js`, the field list in `payload_test.go`, `SchemaVersion` and
PRIVACY.md together.
