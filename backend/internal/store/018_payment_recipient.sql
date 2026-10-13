-- Historical receipts paid the provider. Preserve that destination, including paid bills.
ALTER TABLE bills ADD COLUMN recipient text;
DROP TRIGGER immutable_bill ON bills;
UPDATE bills SET recipient=provider;
ALTER TABLE bills ALTER COLUMN recipient SET NOT NULL;
ALTER TABLE bills ADD CONSTRAINT bills_recipient_nonempty CHECK(recipient<>'');
CREATE OR REPLACE FUNCTION immutable_bill() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.id,NEW.user_id,NEW.wallet,NEW.chain_id,NEW.contract,NEW.token,NEW.provider,NEW.recipient,NEW.amount,NEW.amount_units)
 IS DISTINCT FROM (OLD.id,OLD.user_id,OLD.wallet,OLD.chain_id,OLD.contract,OLD.token,OLD.provider,OLD.recipient,OLD.amount,OLD.amount_units)
 THEN RAISE EXCEPTION 'bill terms are immutable'; END IF;
 IF OLD.state='paid' AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'paid bill is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER immutable_bill BEFORE UPDATE ON bills FOR EACH ROW EXECUTE FUNCTION immutable_bill();
-- Allows the existing testnet worker to finish during a rolling migration.
CREATE FUNCTION default_bill_recipient() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.recipient IS NULL THEN NEW.recipient=NEW.provider; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER default_bill_recipient BEFORE INSERT ON bills FOR EACH ROW EXECUTE FUNCTION default_bill_recipient();
INSERT INTO schema_migrations(version) VALUES(18);
