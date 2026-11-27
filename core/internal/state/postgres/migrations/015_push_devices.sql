-- Native iOS APNs device token registry and idempotent push delivery outbox.
-- Token is globally unique across users so signing in on a shared device
-- reassigns the token cleanly and sign-out deletes it.

CREATE TABLE IF NOT EXISTS nori_push_devices (
    id text PRIMARY KEY,
    user_id text NOT NULL,
    device_id text NOT NULL DEFAULT '',
    token text NOT NULL UNIQUE,
    environment text NOT NULL CHECK (environment IN ('sandbox', 'production')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX IF NOT EXISTS nori_push_devices_user_id
    ON nori_push_devices (user_id);

CREATE INDEX IF NOT EXISTS nori_push_devices_user_device
    ON nori_push_devices (user_id, device_id)
    WHERE device_id <> '';

CREATE TABLE IF NOT EXISTS nori_push_outbox (
    delivery_id text PRIMARY KEY,
    user_id text NOT NULL,
    interaction_id text NOT NULL DEFAULT '',
    collapse_id text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed', 'suppressed')),
    attempts integer NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT '',
    sent_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX IF NOT EXISTS nori_push_outbox_user_created
    ON nori_push_outbox (user_id, created_at);

CREATE INDEX IF NOT EXISTS nori_push_outbox_pending
    ON nori_push_outbox (status, updated_at)
    WHERE status = 'pending';
