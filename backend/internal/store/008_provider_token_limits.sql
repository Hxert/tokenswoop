-- Retain legacy values for historical inspection; current models no longer
-- expose, configure or enforce platform token ceilings.
ALTER TABLE models ALTER COLUMN max_input_tokens DROP NOT NULL;
ALTER TABLE models ALTER COLUMN max_output_tokens DROP NOT NULL;
ALTER TABLE requests ALTER COLUMN input_estimate TYPE bigint;
INSERT INTO schema_migrations(version) VALUES(8) ON CONFLICT DO NOTHING;
