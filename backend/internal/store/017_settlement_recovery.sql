-- Same-nonce replacements retain every signed variant, including the original.
CREATE TABLE chain_tx_replacements (
 root_hash text NOT NULL, tx_hash text PRIMARY KEY, raw_cipher text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX chain_tx_replacements_root ON chain_tx_replacements(root_hash,created_at);
ALTER TABLE bills ADD COLUMN reconcile_from numeric(78,0) NOT NULL DEFAULT 0;
ALTER TABLE payment_attempts ADD COLUMN resolved_tx_hash text;
ALTER TABLE reserve_debt_reports ADD COLUMN resolved_tx_hash text;
ALTER TABLE reserve_approvals ADD COLUMN resolved_tx_hash text;
-- Preserve earlier deployment positions until their owners withdraw them.
ALTER TABLE reserve_accounts DROP CONSTRAINT reserve_accounts_pkey;
ALTER TABLE reserve_accounts ADD PRIMARY KEY(user_id,chain_id,contract);
INSERT INTO schema_migrations(version) VALUES(17);
