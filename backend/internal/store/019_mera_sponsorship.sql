ALTER TABLE auth_challenges ADD COLUMN wallet_source text NOT NULL DEFAULT 'external';
ALTER TABLE sessions ADD COLUMN wallet_source text NOT NULL DEFAULT 'external';

CREATE TABLE mera_transactions (
 id uuid PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES users(id),
 wallet text NOT NULL,
 chain_id bigint NOT NULL,
 implementation text NOT NULL,
 sponsor text NOT NULL,
 action text NOT NULL,
 amount_units numeric(78,0) NOT NULL CHECK(amount_units >= 0),
 operation_id text NOT NULL UNIQUE,
 deadline bigint NOT NULL,
 authorization_nonce bigint,
 authorization_payload jsonb,
 signature text,
 state text NOT NULL DEFAULT 'prepared' CHECK(state IN ('prepared','queued','submitted','confirmed','reverted','expired','cancelled','rejected','review')),
 raw_tx text,
 tx_hash text,
 sponsor_nonce bigint,
 gas_budget numeric(78,0) NOT NULL DEFAULT 0,
 error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 broadcast_at timestamptz,
 completed_at timestamptz
);
CREATE INDEX mera_queue ON mera_transactions(created_at) WHERE state IN ('queued','submitted');
CREATE INDEX mera_user_history ON mera_transactions(user_id, created_at);
CREATE UNIQUE INDEX mera_one_active_per_wallet ON mera_transactions(chain_id,wallet) WHERE state IN ('prepared','queued','submitted','review');
INSERT INTO schema_migrations(version) VALUES(19);
