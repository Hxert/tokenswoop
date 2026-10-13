ALTER TABLE requests ADD COLUMN IF NOT EXISTS cache_write_1h_tokens bigint NOT NULL DEFAULT 0;
INSERT INTO schema_migrations(version) VALUES(2) ON CONFLICT DO NOTHING;
