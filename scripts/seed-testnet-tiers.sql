-- Explicitly selected testnet examples, 2026-10-08. Does not overwrite custom tiers.
BEGIN;
INSERT INTO tiers(id,discount_bps,rpm,concurrency,min_principal,settlement_threshold) VALUES
 ('builder',300,120,5,50,0.05),('scale',800,300,10,200,0.20),('pro',1500,600,20,1000,1.00)
ON CONFLICT DO NOTHING;
INSERT INTO tier_versions(tier_id,version,snapshot)
 SELECT id,version,to_jsonb(tiers) FROM tiers ON CONFLICT DO NOTHING;
INSERT INTO audit_events(id,action,target,details)
 VALUES(gen_random_uuid(),'tier.testnet_examples','testnet', '{"thresholds":[50,200,1000],"discount_bps":[300,800,1500],"rpm":[120,300,600],"concurrency":[5,10,20]}');
COMMIT;
