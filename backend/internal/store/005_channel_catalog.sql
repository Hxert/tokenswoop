ALTER TABLE models ADD COLUMN pricing_configured boolean NOT NULL DEFAULT true;
ALTER TABLE models ADD CONSTRAINT models_enabled_requires_pricing CHECK (NOT enabled OR pricing_configured);
ALTER TABLE channels ADD COLUMN revision integer NOT NULL DEFAULT 1 CHECK (revision > 0);
INSERT INTO schema_migrations(version) VALUES(5);
