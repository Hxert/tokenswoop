UPDATE bills SET insufficient_balance_at=now() WHERE state<>'paid' AND error_code='insufficient_allowance' AND insufficient_balance_at IS NULL;
CREATE TABLE deployment_network (
 id integer PRIMARY KEY CHECK(id=1), chain_id bigint NOT NULL CHECK(chain_id IN(10143,143))
);
CREATE TABLE reserve_debt_reports (
 id uuid PRIMARY KEY, user_id uuid NOT NULL REFERENCES users(id), wallet text NOT NULL,
 chain_id bigint NOT NULL, contract text NOT NULL, operator text NOT NULL,
 epoch numeric(78,0) NOT NULL, items jsonb NOT NULL, calldata text NOT NULL,
 tx_hash text NOT NULL UNIQUE, nonce numeric(78,0) NOT NULL, raw_cipher text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN('pending','confirmed','reverted')),
 block_number text, block_hash text, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(chain_id,operator,nonce)
);
CREATE UNIQUE INDEX one_pending_reserve_report ON reserve_debt_reports(user_id) WHERE state='pending';
CREATE TABLE reserve_debt_items (
 user_id uuid NOT NULL REFERENCES users(id), request_id uuid NOT NULL REFERENCES requests(id),
 chain_id bigint NOT NULL, contract text NOT NULL, debt_id text NOT NULL,
 epoch numeric(78,0) NOT NULL, amount_units numeric(78,0) NOT NULL CHECK(amount_units>=0),
 PRIMARY KEY(user_id,chain_id,contract,request_id), UNIQUE(chain_id,contract,user_id,debt_id)
);
INSERT INTO schema_migrations(version) VALUES(16);
