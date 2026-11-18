-- model_router's target model, pinned at session start (plan item 7.2), so a gateway
-- restart mid-session keeps routing to the same model. A model name only; '' = idle.
ALTER TABLE session_pins ADD COLUMN route_to TEXT NOT NULL DEFAULT '';
-- The effort level the request asked for (output_config.effort: low, medium, high,
-- xhigh or max), for the effort cache-break check (plan item 7.4). '' = not set.
ALTER TABLE requests ADD COLUMN effort TEXT NOT NULL DEFAULT '';
