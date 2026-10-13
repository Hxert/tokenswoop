-- Keep the first balance failure across retries and transaction submission.
-- Only an unpaid bill carrying this evidence pauses new paid requests.
ALTER TABLE bills ADD COLUMN insufficient_balance_at timestamptz;
UPDATE bills SET insufficient_balance_at=now()
 WHERE state<>'paid' AND error_code='insufficient_balance';
CREATE INDEX bills_insufficient_balance ON bills(user_id)
 WHERE state<>'paid' AND insufficient_balance_at IS NOT NULL;
INSERT INTO schema_migrations(version) VALUES(13);
