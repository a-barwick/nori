-- Personal Agent Computer lifecycle, live desktop sessions, and Library/forget file links.
-- Kept independent from nori_task_leases so VM lifecycle and account teardown never couple to task claims.

CREATE TABLE IF NOT EXISTS nori_user_computers (
    user_id text PRIMARY KEY,
    computer_id text NOT NULL UNIQUE,
    provider text NOT NULL CHECK (provider IN ('sprites', 'docker', 'fake')),
    provider_id text NOT NULL,
    status text NOT NULL CHECK (status IN ('provisioning', 'ready', 'sleeping', 'resetting', 'deleting', 'deleted', 'error')),
    baseline_checkpoint_id text NOT NULL DEFAULT '',
    bootstrap_version text NOT NULL DEFAULT '',
    last_active_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS nori_user_computers_status_updated
    ON nori_user_computers (status, updated_at);

CREATE TABLE IF NOT EXISTS nori_computer_sessions (
    id text PRIMARY KEY,
    user_id text NOT NULL,
    computer_id text NOT NULL REFERENCES nori_user_computers(computer_id) ON DELETE CASCADE,
    mode text NOT NULL CHECK (mode IN ('view', 'control')),
    task_id text NOT NULL DEFAULT '',
    interaction_id text NOT NULL DEFAULT '',
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    ended_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_activity_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS nori_computer_sessions_user_active
    ON nori_computer_sessions (user_id)
    WHERE ended_at IS NULL;
CREATE INDEX IF NOT EXISTS nori_computer_sessions_expires
    ON nori_computer_sessions (expires_at);

CREATE TABLE IF NOT EXISTS nori_computer_file_links (
    user_id text NOT NULL,
    workspace_path text NOT NULL,
    result_id text NOT NULL DEFAULT '',
    entity_tags jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(entity_tags) = 'array'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (user_id, workspace_path)
);
CREATE INDEX IF NOT EXISTS nori_computer_file_links_result
    ON nori_computer_file_links (user_id, result_id)
    WHERE result_id <> '';
