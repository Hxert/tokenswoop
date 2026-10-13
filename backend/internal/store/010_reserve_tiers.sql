ALTER TABLE tiers ADD COLUMN min_principal numeric(24,6) CHECK(min_principal>=0);
ALTER TABLE tiers ADD COLUMN version integer NOT NULL DEFAULT 1;
UPDATE tiers SET min_principal=0 WHERE id='free';
CREATE UNIQUE INDEX tier_principal_threshold ON tiers(min_principal) WHERE min_principal IS NOT NULL;
ALTER TABLE users ADD COLUMN tier_override boolean NOT NULL DEFAULT false;
UPDATE users SET tier_override=true WHERE tier<>'free';
CREATE TABLE tier_versions (
 tier_id text NOT NULL REFERENCES tiers(id), version integer NOT NULL, snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(tier_id,version)
);
INSERT INTO tier_versions SELECT id,version,to_jsonb(tiers),now() FROM tiers;

CREATE TABLE reserve_accounts (
 user_id uuid PRIMARY KEY REFERENCES users(id), chain_id bigint NOT NULL, contract text NOT NULL, token text NOT NULL,
 principal numeric(78,0) NOT NULL CHECK(principal>=0), withdrawal_pending boolean NOT NULL,
 snapshot jsonb NOT NULL, block_number numeric(78,0) NOT NULL, block_hash text NOT NULL,
 checked_at timestamptz NOT NULL
);
CREATE TABLE reserve_approvals (
 id uuid PRIMARY KEY, user_id uuid NOT NULL REFERENCES users(id), wallet text NOT NULL,
 chain_id bigint NOT NULL, contract text NOT NULL, operator text NOT NULL,
 request_id numeric(78,0) NOT NULL, amount_units numeric(78,0) NOT NULL,
 operator_version numeric(78,0) NOT NULL, approved_until bigint NOT NULL,
 tx_hash text NOT NULL UNIQUE, nonce numeric(78,0) NOT NULL, raw_cipher text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN('pending','confirmed','reverted')),
 block_number text, block_hash text, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(chain_id,operator,nonce)
);
CREATE UNIQUE INDEX one_pending_reserve_approval ON reserve_approvals(user_id) WHERE state='pending';

ALTER TABLE requests ADD COLUMN waived_amount numeric(38,18) NOT NULL DEFAULT 0 CHECK(waived_amount>=0);
ALTER TABLE requests DROP CONSTRAINT requests_charge_breakdown_check;
ALTER TABLE requests ADD CONSTRAINT requests_charge_breakdown_check
 CHECK(state<>'completed' OR (amount>=0 AND credit_amount>=0 AND amount=credit_amount+wallet_paid_amount+unpaid_amount+waived_amount));
INSERT INTO schema_migrations(version) VALUES(10);
