### What the gateway optimizers did in this study

| condition | runs | input tokens (measured) | tokens removed (estimated) | share of input removed (estimated) | requests changed | runs with a gateway optimizer on |
|---|---|---|---|---|---|---|
| passthrough | 30 | 7,723,652 | 0 | 0.00% | 0.0% | 0 |
| passthrough_plugin | 30 | 7,753,044 | 0 | 0.00% | 0.0% | 0 |
| trove_full | 30 | 10,476,900 | 0 | 0.00% | 0.0% | 30 |

This table counts the gateway only; what the hooks kept out of the context is in the next table. Tokens removed are estimates (removed bytes over a bytes-per-token ratio). They are not a bill saving: the bill is compared in the study report, where cost is also estimated (list price applied to the usage the gateway recorded) and pass/fail is measured. Say what the study does not cover: these tasks only, one model, one agent version.

### What the hooks did in this study

condition | runs | hook acted in | only noted what it would do | tokens kept out (estimated) | acts by hook
passthrough | 30 | 0 (0%) | 0 | 0 | -
passthrough_plugin | 30 | 2 (7%) | 0 | 10 | noise_normalizer 2
trove_full | 30 | 2 (7%) | 0 | 10 | noise_normalizer 2

If a single-hook condition shows a hook acted in only a small share of runs, a result of 'no clear difference' says the hook rarely had anything to do on these tasks, not that it does nothing.
