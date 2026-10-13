-- NULL means no per-key spending cap. Existing positive budgets stay unchanged.
ALTER TABLE api_keys ALTER COLUMN budget DROP NOT NULL;
INSERT INTO schema_migrations(version) VALUES (9);
