CREATE TABLE testnet_faucet_claims (
 id uuid PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES users(id),
 wallet text NOT NULL,
 chain_id bigint NOT NULL CHECK(chain_id=10143),
 token text NOT NULL,
 sponsor text NOT NULL,
 amount_units bigint NOT NULL DEFAULT 10000000 CHECK(amount_units=10000000),
 ip_digest text NOT NULL,
 token_digest text NOT NULL UNIQUE,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','submitted','confirmed','failed','review')),
 raw_tx text,
 tx_hash text UNIQUE,
 sponsor_nonce bigint,
 error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 broadcast_at timestamptz,
 completed_at timestamptz
);
CREATE INDEX faucet_wallet_history ON testnet_faucet_claims(wallet,created_at DESC);
CREATE INDEX faucet_ip_history ON testnet_faucet_claims(ip_digest,created_at DESC);
CREATE INDEX faucet_queue ON testnet_faucet_claims(created_at) WHERE state IN ('queued','submitted','review');
CREATE UNIQUE INDEX faucet_one_pending ON testnet_faucet_claims(wallet) WHERE state IN ('queued','submitted','review');
INSERT INTO schema_migrations(version) VALUES(20);
