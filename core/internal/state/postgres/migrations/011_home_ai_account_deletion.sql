CREATE OR REPLACE VIEW nori_home_ai_accounts WITH (security_barrier = true) AS
SELECT id FROM nori_records
WHERE shard = 'users' AND collection = 'users'
    AND COALESCE((data->>'reset_pending')::boolean, false) = false
    AND COALESCE((data->'delete_pending') = 'true'::jsonb, false) = false
    AND (id = 'owner' OR id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$');
