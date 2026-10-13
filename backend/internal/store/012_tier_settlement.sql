ALTER TABLE tiers ADD COLUMN settlement_threshold numeric(24,6) NOT NULL DEFAULT 0.01
 CHECK(settlement_threshold>=0.000001 AND settlement_threshold<=100);
UPDATE tiers SET settlement_threshold=CASE id
 WHEN 'builder' THEN 0.05 WHEN 'scale' THEN 0.20 WHEN 'pro' THEN 1.00 ELSE 0.01 END,
 version=version+1;
INSERT INTO tier_versions(tier_id,version,snapshot) SELECT id,version,to_jsonb(tiers) FROM tiers;

-- Completion time starts the settlement window. Later requests and payment
-- updates must not move an older unpaid request's deadline.
ALTER TABLE requests ADD COLUMN completed_at timestamptz;
UPDATE requests SET completed_at=updated_at WHERE state='completed';
CREATE INDEX requests_unpaid_completion ON requests(user_id,completed_at)
 WHERE state='completed' AND unpaid_amount>0;
INSERT INTO schema_migrations(version) VALUES(12);
