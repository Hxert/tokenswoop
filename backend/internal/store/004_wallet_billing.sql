ALTER TABLE requests ADD COLUMN wallet_paid_amount numeric(38,18) NOT NULL DEFAULT 0 CHECK(wallet_paid_amount>=0);
ALTER TABLE requests DROP CONSTRAINT requests_charge_breakdown_check;
ALTER TABLE requests ADD CONSTRAINT requests_charge_breakdown_check
 CHECK(state<>'completed' OR (amount>=0 AND credit_amount>=0 AND amount=credit_amount+wallet_paid_amount+unpaid_amount));

CREATE TABLE payment_accounts (
 user_id uuid PRIMARY KEY REFERENCES users(id),
 chain_id bigint NOT NULL, contract text NOT NULL, token text NOT NULL, provider text NOT NULL,
 balance numeric(78,0) NOT NULL CHECK(balance>=0), allowance numeric(78,0) NOT NULL CHECK(allowance>=0),
 block_number text NOT NULL, checked_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE bills (
 id text PRIMARY KEY CHECK(id ~ '^0x[0-9a-f]{64}$'), user_id uuid NOT NULL REFERENCES users(id), wallet text NOT NULL,
 chain_id bigint NOT NULL, contract text NOT NULL, token text NOT NULL, provider text NOT NULL,
 amount numeric(38,18) NOT NULL CHECK(amount>0), amount_units numeric(78,0) NOT NULL CHECK(amount_units>0),
 state text NOT NULL DEFAULT 'queued' CHECK(state IN('queued','submitted','blocked','review','paid')),
 error_code text NOT NULL DEFAULT '', tx_hash text, block_number text, block_hash text,
 next_attempt_at timestamptz NOT NULL DEFAULT now(), created_at timestamptz NOT NULL DEFAULT now(), paid_at timestamptz,
 CHECK(amount=amount_units/1000000)
);
CREATE UNIQUE INDEX one_open_bill_per_user ON bills(user_id) WHERE state<>'paid';
CREATE INDEX bills_due ON bills(next_attempt_at) WHERE state<>'paid';
CREATE TABLE bill_items (
 bill_id text NOT NULL REFERENCES bills(id), request_id uuid NOT NULL REFERENCES requests(id),
 amount numeric(38,18) NOT NULL CHECK(amount>0), PRIMARY KEY(bill_id,request_id)
);
CREATE INDEX bill_items_request ON bill_items(request_id);
CREATE TABLE payment_attempts (
 id uuid PRIMARY KEY, bill_id text NOT NULL REFERENCES bills(id), tx_hash text NOT NULL UNIQUE,
 chain_id bigint NOT NULL, provider text NOT NULL, nonce numeric(78,0) NOT NULL,
 raw_cipher text NOT NULL, state text NOT NULL DEFAULT 'pending' CHECK(state IN('pending','reverted','paid')),
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(chain_id,provider,nonce)
);
CREATE UNIQUE INDEX one_pending_attempt_per_bill ON payment_attempts(bill_id) WHERE state='pending';

-- Identity, denomination and request allocation cannot change after issuance.
CREATE FUNCTION immutable_bill() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.id,NEW.user_id,NEW.wallet,NEW.chain_id,NEW.contract,NEW.token,NEW.provider,NEW.amount,NEW.amount_units)
 IS DISTINCT FROM (OLD.id,OLD.user_id,OLD.wallet,OLD.chain_id,OLD.contract,OLD.token,OLD.provider,OLD.amount,OLD.amount_units)
 THEN RAISE EXCEPTION 'bill terms are immutable'; END IF;
 IF OLD.state='paid' AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'paid bill is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER immutable_bill BEFORE UPDATE ON bills FOR EACH ROW EXECUTE FUNCTION immutable_bill();
INSERT INTO schema_migrations(version) VALUES(4);
