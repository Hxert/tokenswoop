ALTER TABLE models ADD COLUMN IF NOT EXISTS pricing jsonb NOT NULL DEFAULT '{}';
ALTER TABLE models ADD COLUMN IF NOT EXISTS tokenizer text NOT NULL DEFAULT 'auto';
ALTER TABLE requests ADD COLUMN IF NOT EXISTS client_protocol text NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN IF NOT EXISTS upstream_protocol text NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN IF NOT EXISTS operation text NOT NULL DEFAULT 'generate';
ALTER TABLE requests ADD COLUMN IF NOT EXISTS is_stream boolean NOT NULL DEFAULT false;
ALTER TABLE requests ADD COLUMN IF NOT EXISTS input_estimate integer;
ALTER TABLE requests ADD COLUMN IF NOT EXISTS estimate_method text NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN IF NOT EXISTS first_token_ms bigint;
ALTER TABLE requests ADD COLUMN IF NOT EXISTS generation_ms bigint;
ALTER TABLE requests ADD COLUMN IF NOT EXISTS pricing_trace jsonb;
ALTER TABLE requests ADD COLUMN IF NOT EXISTS tool_calls jsonb NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS requests_ops_created_idx ON requests(created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS requests_ops_channel_idx ON requests(channel_id,created_at DESC);
CREATE TABLE IF NOT EXISTS response_handles (
 digest text PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES users(id),
 request_id uuid NOT NULL REFERENCES requests(id),
 model_id text NOT NULL REFERENCES models(id),
 channel_id uuid NOT NULL REFERENCES channels(id),
 channel_revision integer NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS response_handles_expiry_idx ON response_handles(expires_at);
CREATE TABLE IF NOT EXISTS operations_tasks (
 id uuid PRIMARY KEY,
 actor_id uuid NOT NULL REFERENCES users(id),
 kind text NOT NULL,
 state text NOT NULL DEFAULT 'queued',
 parameters jsonb NOT NULL DEFAULT '{}',
 processed bigint NOT NULL DEFAULT 0,
 error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 finished_at timestamptz
);
INSERT INTO schema_migrations(version) VALUES(7) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS runtime_instances (
 id uuid PRIMARY KEY,
 role text NOT NULL,
 started_at timestamptz NOT NULL DEFAULT now(),
 last_seen timestamptz NOT NULL DEFAULT now()
);
