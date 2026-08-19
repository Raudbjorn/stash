-- Retain task inputs and terminal handler responses for debugging.
ALTER TABLE task_history ADD COLUMN input_params TEXT;
ALTER TABLE task_history ADD COLUMN result_json TEXT;
