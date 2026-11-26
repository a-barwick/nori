-- Enforce case-insensitive email uniqueness for user directory records at the database layer.
-- Protects both self-service provisioning and administrative CLI user creation.
CREATE UNIQUE INDEX IF NOT EXISTS nori_users_email_lower_uidx
    ON nori_records_users ((lower(data->>'email')))
    WHERE collection = 'users' AND COALESCE(data->>'email', '') <> '';
