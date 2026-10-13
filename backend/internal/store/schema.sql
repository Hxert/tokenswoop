CREATE TABLE IF NOT EXISTS schema_migrations (version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS users (
 id uuid PRIMARY KEY, wallet text UNIQUE NOT NULL, role text NOT NULL CHECK(role IN ('user','admin')) DEFAULT 'user',
 status text NOT NULL CHECK(status IN ('active','suspended')) DEFAULT 'active', tier text NOT NULL DEFAULT 'free',
 credit_balance numeric(38,18) NOT NULL DEFAULT 0 CHECK(credit_balance>=0), credit_reserved numeric(38,18) NOT NULL DEFAULT 0 CHECK(credit_reserved>=0),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS tiers (
 id text PRIMARY KEY, discount_bps integer NOT NULL DEFAULT 0 CHECK(discount_bps BETWEEN 0 AND 10000),
 rpm integer NOT NULL CHECK(rpm BETWEEN 1 AND 100000), concurrency integer NOT NULL CHECK(concurrency BETWEEN 1 AND 1000)
);
INSERT INTO tiers(id,rpm,concurrency) VALUES ('free',30,2) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS auth_challenges (id uuid PRIMARY KEY, wallet text NOT NULL, message text NOT NULL, expires_at timestamptz NOT NULL, consumed boolean NOT NULL DEFAULT false);
CREATE TABLE IF NOT EXISTS sessions (token_hash text PRIMARY KEY, user_id uuid NOT NULL REFERENCES users(id), csrf_hash text NOT NULL, expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS api_keys (
 id uuid PRIMARY KEY, user_id uuid NOT NULL REFERENCES users(id), name text NOT NULL, prefix text NOT NULL, token_hash text UNIQUE NOT NULL,
 status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','revoked')), allowed_models text[] NOT NULL DEFAULT '{}',
 budget numeric(38,18) NOT NULL CHECK(budget>0), spent numeric(38,18) NOT NULL DEFAULT 0, reserved numeric(38,18) NOT NULL DEFAULT 0,
 expires_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS models (
 id text PRIMARY KEY, name text NOT NULL, vendor text NOT NULL, enabled boolean NOT NULL DEFAULT true,
 tools boolean NOT NULL DEFAULT false, structured_output boolean NOT NULL DEFAULT false,
 max_input_tokens integer NOT NULL CHECK(max_input_tokens BETWEEN 1 AND 1000000), max_output_tokens integer NOT NULL CHECK(max_output_tokens BETWEEN 1 AND 131072),
 price_version integer NOT NULL DEFAULT 1,
 input_price numeric(24,12) NOT NULL CHECK(input_price>=0), output_price numeric(24,12) NOT NULL CHECK(output_price>=0),
 cache_read_price numeric(24,12) NOT NULL CHECK(cache_read_price>=0), cache_write_price numeric(24,12) NOT NULL CHECK(cache_write_price>=0), cache_write_1h_price numeric(24,12) NOT NULL CHECK(cache_write_1h_price>=0),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS model_prices (model_id text NOT NULL REFERENCES models(id), version integer NOT NULL, snapshot jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(model_id,version));
CREATE TABLE IF NOT EXISTS channels (
 id uuid PRIMARY KEY, name text NOT NULL, protocol text NOT NULL CHECK(protocol IN ('openai','openai_responses','claude','gemini')),
 base_url text NOT NULL, secret_cipher text NOT NULL, model_map jsonb NOT NULL DEFAULT '{}', priority integer NOT NULL DEFAULT 0,
 enabled boolean NOT NULL DEFAULT true, failure_count integer NOT NULL DEFAULT 0, disabled_until timestamptz, last_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS requests (
 id uuid PRIMARY KEY, user_id uuid NOT NULL REFERENCES users(id), key_id uuid REFERENCES api_keys(id), model_id text NOT NULL REFERENCES models(id),
 state text NOT NULL CHECK(state IN ('reserved','in_flight','completed','failed','usage_unknown')),
 reserved numeric(38,18) NOT NULL, amount numeric(38,18) NOT NULL DEFAULT 0, credit_amount numeric(38,18) NOT NULL DEFAULT 0,
 input_tokens bigint NOT NULL DEFAULT 0, output_tokens bigint NOT NULL DEFAULT 0, cache_read_tokens bigint NOT NULL DEFAULT 0, cache_write_tokens bigint NOT NULL DEFAULT 0,
 price_snapshot jsonb NOT NULL, tier_snapshot jsonb NOT NULL, usage_source text NOT NULL DEFAULT '',
 channel_id uuid REFERENCES channels(id), status_code integer NOT NULL DEFAULT 0, error_code text NOT NULL DEFAULT '', latency_ms bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS requests_user_time ON requests(user_id,created_at DESC);
CREATE TABLE IF NOT EXISTS request_attempts (id uuid PRIMARY KEY, request_id uuid NOT NULL REFERENCES requests(id), channel_id uuid NOT NULL REFERENCES channels(id), status_code integer NOT NULL, outcome text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS credit_entries (id uuid PRIMARY KEY, user_id uuid NOT NULL REFERENCES users(id), request_id uuid REFERENCES requests(id), amount numeric(38,18) NOT NULL, kind text NOT NULL, reference text UNIQUE NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS audit_events (id uuid PRIMARY KEY, actor_id uuid REFERENCES users(id), action text NOT NULL, target text NOT NULL, details jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS outbox_events (id uuid PRIMARY KEY, kind text NOT NULL, request_id uuid REFERENCES requests(id), payload jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(), processed_at timestamptz);
CREATE TABLE IF NOT EXISTS worker_heartbeats (id text PRIMARY KEY, last_seen timestamptz NOT NULL DEFAULT now());
INSERT INTO schema_migrations(version) VALUES(1) ON CONFLICT DO NOTHING;
