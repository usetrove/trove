-- Session kind and upstream name per request. Both are short labels, never content.
--   kind: 'interactive', 'automated' or 'unknown'. Set only from what the user declared
--         (the X-Trove-Kind header); Trove never guesses it from a User-Agent.
--   upstream: the configured name of an OpenAI-compatible server ('' for the built-in providers).
ALTER TABLE requests ADD COLUMN kind TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE requests ADD COLUMN upstream TEXT NOT NULL DEFAULT '';
