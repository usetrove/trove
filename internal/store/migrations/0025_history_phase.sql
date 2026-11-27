-- Where each history request sits in its agent's conversation, for the rebuild and thinking ledgers
-- (internal/analyze/ledger.go, docs/analyze.md). Hashes, short codes and sizes only (PRINCIPLES.md
-- rule 7). NULL = read by a parser that did not record it.
--   agent_key   hash of the subagent's id; '' for the main conversation
--   phase       what came right before the request: prompt | tool_result | compact | other
--   compacted   1 when a compaction boundary came since the agent's previous request
--   text_bytes  byte size of the response's visible text blocks; the text is never stored
ALTER TABLE history_requests ADD COLUMN agent_key  TEXT;
ALTER TABLE history_requests ADD COLUMN phase      TEXT;
ALTER TABLE history_requests ADD COLUMN compacted  INTEGER;
ALTER TABLE history_requests ADD COLUMN text_bytes INTEGER;

-- Parser state carried from one scan of a file to the next, so a request read after the checkpoint
-- still knows what came before it. Codes only.
ALTER TABLE history_files ADD COLUMN carry_phase     TEXT    NOT NULL DEFAULT '';
ALTER TABLE history_files ADD COLUMN carry_compacted INTEGER NOT NULL DEFAULT 0;
