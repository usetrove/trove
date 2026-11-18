-- Per-marker cache lifetimes pinned at session start for cache_breakpoints
-- (internal/optimize/breakpoints.go). Metadata only: a JSON object such as
-- {"tools":"1h","system":"1h"} whose values are "5m" or "1h". NULL means every
-- marker used the 5-minute default (or the session was pinned before this existed).
ALTER TABLE session_pins ADD COLUMN cache_ttl TEXT;
