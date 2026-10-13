-- Retire promotional balances without turning prior discounts into debt.
-- These archives are historical evidence only; no runtime code uses them.
CREATE TABLE legacy_credit_balances (
 user_id uuid PRIMARY KEY REFERENCES users(id), balance numeric(38,18) NOT NULL,
 retired_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO legacy_credit_balances(user_id,balance)
 SELECT id,credit_balance FROM users WHERE credit_balance<>0;
ALTER TABLE credit_entries RENAME TO legacy_credit_entries;

INSERT INTO audit_events(id,action,target,details)
 SELECT gen_random_uuid(),'migration.retire_credit',id::text,
 jsonb_build_object('amount',credit_amount::text,'classification','historical_waiver')
 FROM requests WHERE credit_amount<>0;

ALTER TABLE requests DROP CONSTRAINT requests_charge_breakdown_check;
UPDATE requests SET waived_amount=waived_amount+credit_amount WHERE credit_amount<>0;
ALTER TABLE requests DROP COLUMN credit_amount;
ALTER TABLE users DROP COLUMN credit_balance;
ALTER TABLE requests ADD CONSTRAINT requests_charge_breakdown_check
 CHECK(state<>'completed' OR (amount>=0 AND amount=wallet_paid_amount+unpaid_amount+waived_amount));
INSERT INTO schema_migrations(version) VALUES(11);
