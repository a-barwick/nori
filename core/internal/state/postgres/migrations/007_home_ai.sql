CREATE VIEW nori_home_ai_accounts WITH (security_barrier = true) AS
SELECT id FROM nori_records
WHERE shard='users' AND collection='users'
  AND COALESCE((data->>'reset_pending')::boolean,false)=false
  AND (id='owner' OR id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$');

CREATE TABLE nori_home_ai_grants (
    id text PRIMARY KEY,
    device_code_hash bytea NOT NULL UNIQUE,
    user_code_hash bytea NOT NULL UNIQUE,
    user_code text NOT NULL,
    device_name text NOT NULL,
    peer_hash bytea NOT NULL,
    status text NOT NULL CHECK (status IN ('pending', 'approved', 'redeemed', 'denied')),
    user_id text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    last_polled timestamptz,
    poll_interval integer NOT NULL DEFAULT 5
);
CREATE INDEX nori_home_ai_grants_expiry ON nori_home_ai_grants (expires_at);
CREATE INDEX nori_home_ai_grants_peer ON nori_home_ai_grants (peer_hash, created_at);

CREATE TABLE nori_home_ai_devices (
    id text PRIMARY KEY,
    user_id text NOT NULL,
    name text NOT NULL,
    refresh_hash bytea NOT NULL UNIQUE,
    access_hash bytea NOT NULL UNIQUE,
    access_expires_at timestamptz NOT NULL,
    refresh_expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    generation bigint NOT NULL DEFAULT 1,
    connection_id text,
    last_seen timestamptz,
    ollama_available boolean NOT NULL DEFAULT false,
    models jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(models) = 'array'),
    version text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX nori_home_ai_devices_user ON nori_home_ai_devices (user_id);

CREATE TABLE nori_home_ai_refresh_history (
    token_hash bytea PRIMARY KEY,
    device_id text NOT NULL REFERENCES nori_home_ai_devices(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL
);
CREATE INDEX nori_home_ai_refresh_history_expiry ON nori_home_ai_refresh_history (expires_at);

CREATE TABLE nori_home_ai_requests (
    id text PRIMARY KEY,
    attempt_id text NOT NULL,
    user_id text NOT NULL,
    device_id text NOT NULL REFERENCES nori_home_ai_devices(id) ON DELETE CASCADE,
    generation bigint NOT NULL,
    connection_id text,
    operation text NOT NULL CHECK (operation IN ('chat', 'responses')),
    model text NOT NULL,
    digest text NOT NULL,
    payload bytea NOT NULL,
    status text NOT NULL CHECK (status IN ('pending', 'dispatched', 'running', 'complete', 'failed', 'cancelled')),
    http_status integer,
    content_type text,
    error_code text,
    next_sequence bigint NOT NULL DEFAULT 1,
    total_bytes bigint NOT NULL DEFAULT 0,
    deadline timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE UNIQUE INDEX nori_home_ai_active_per_device ON nori_home_ai_requests (device_id) WHERE status IN ('pending', 'dispatched', 'running');
CREATE INDEX nori_home_ai_request_expiry ON nori_home_ai_requests (deadline);

CREATE TABLE nori_home_ai_chunks (
    request_id text NOT NULL REFERENCES nori_home_ai_requests(id) ON DELETE CASCADE,
    attempt_id text NOT NULL,
    sequence bigint NOT NULL,
    payload bytea NOT NULL,
    PRIMARY KEY (request_id, attempt_id, sequence)
);

CREATE TABLE nori_home_ai_pairing_attempts (
    user_id text NOT NULL,
    bucket timestamptz NOT NULL,
    attempts integer NOT NULL,
    PRIMARY KEY (user_id, bucket)
);
