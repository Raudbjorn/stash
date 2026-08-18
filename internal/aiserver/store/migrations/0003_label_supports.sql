-- Scene-level VLM label support diagnostics.
-- Stored once per analysis run on a diagnostic aggregate row so unresolved
-- taxonomy labels remain queryable alongside labels linked to local tags.
ALTER TABLE ai_result_aggregates ADD COLUMN label_supports TEXT;
