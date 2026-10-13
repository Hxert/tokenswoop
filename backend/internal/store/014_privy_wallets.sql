CREATE TABLE auth_identities (
    provider text NOT NULL CHECK (provider = 'privy'),
    app_id text NOT NULL,
    subject text NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id),
    wallet_kind text NOT NULL CHECK (wallet_kind IN ('embedded', 'external')),
    wallet_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(provider, app_id, subject),
    UNIQUE(provider, app_id, user_id)
);

-- Only explicit, user-signed wallet operations. API bills remain in their existing journal.
CREATE TABLE wallet_transactions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id),
    app_id text NOT NULL,
    wallet_id text NOT NULL,
    wallet text NOT NULL,
    action text NOT NULL,
    amount_units text NOT NULL,
    payload jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    state text NOT NULL DEFAULT 'prepared' CHECK (state IN ('prepared', 'pending', 'submitted', 'rejected')),
    tx_hash text,
    privy_transaction_id text,
    error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX wallet_transactions_user_date ON wallet_transactions(user_id, created_at DESC);
INSERT INTO schema_migrations(version) VALUES(14);
