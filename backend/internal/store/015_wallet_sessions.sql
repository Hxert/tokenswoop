-- Login providers prove control of the selected wallet. They never select a user.
-- Sponsorship proof belongs to this session, not to a persistent identity binding.
ALTER TABLE sessions
    ADD COLUMN privy_app_id text NOT NULL DEFAULT '',
    ADD COLUMN privy_subject text NOT NULL DEFAULT '',
    ADD COLUMN privy_wallet_id text NOT NULL DEFAULT '';

-- auth_identities is retained as historical data, but is no longer read or written.
-- Existing users, API keys, bills, reserve positions and sessions remain intact.
INSERT INTO schema_migrations(version) VALUES(15);
