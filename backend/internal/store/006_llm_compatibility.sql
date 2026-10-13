ALTER TABLE channels ADD COLUMN IF NOT EXISTS options jsonb NOT NULL DEFAULT '{}';
ALTER TABLE request_attempts ADD COLUMN IF NOT EXISTS diagnostics jsonb NOT NULL DEFAULT '[]';
ALTER TABLE requests ADD COLUMN IF NOT EXISTS upstream_deadline_at timestamptz;
INSERT INTO schema_migrations(version) VALUES(6) ON CONFLICT DO NOTHING;
