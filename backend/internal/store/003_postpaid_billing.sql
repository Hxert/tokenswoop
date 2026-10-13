-- Legacy holds never reduced credit_balance or increased key spent. Removing
-- them must not refund credit or alter already completed usage charges.
INSERT INTO audit_events(id,actor_id,action,target,details)
 SELECT gen_random_uuid(),id,'billing.remove_legacy_hold',id::text,
 jsonb_build_object('entity','user','previous_hold',credit_reserved)
 FROM users WHERE credit_reserved<>0;
INSERT INTO audit_events(id,actor_id,action,target,details)
 SELECT gen_random_uuid(),user_id,'billing.remove_legacy_hold',id::text,
 jsonb_build_object('entity','key','previous_hold',reserved)
 FROM api_keys WHERE reserved<>0;
INSERT INTO audit_events(id,actor_id,action,target,details)
 SELECT gen_random_uuid(),user_id,'billing.remove_legacy_hold',id::text,
 jsonb_build_object('entity','request','previous_hold',reserved,'previous_state',state)
 FROM requests WHERE reserved<>0 AND state IN('reserved','in_flight','usage_unknown');

ALTER TABLE users ADD COLUMN unpaid_balance numeric(38,18) NOT NULL DEFAULT 0 CHECK(unpaid_balance>=0);
ALTER TABLE requests ADD COLUMN unpaid_amount numeric(38,18) NOT NULL DEFAULT 0 CHECK(unpaid_amount>=0);
ALTER TABLE users DROP COLUMN credit_reserved;
ALTER TABLE api_keys DROP COLUMN reserved;
ALTER TABLE requests DROP COLUMN reserved;
ALTER TABLE requests DROP CONSTRAINT requests_state_check;
UPDATE requests SET state='accepted' WHERE state='reserved';
ALTER TABLE requests ADD CONSTRAINT requests_state_check CHECK(state IN('accepted','in_flight','completed','failed','usage_unknown'));
ALTER TABLE requests ADD CONSTRAINT requests_charge_breakdown_check
 CHECK(state<>'completed' OR (amount>=0 AND credit_amount>=0 AND amount=credit_amount+unpaid_amount));
INSERT INTO schema_migrations(version) VALUES(3);
