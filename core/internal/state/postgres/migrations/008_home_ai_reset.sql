CREATE FUNCTION nori_purge_home_ai_account(account_id text)
RETURNS void
LANGUAGE SQL
BEGIN ATOMIC
    DELETE FROM nori_home_ai_grants WHERE user_id = account_id;
    WITH removed AS (
        DELETE FROM nori_home_ai_devices WHERE user_id = account_id RETURNING id
    )
    SELECT pg_notify('nori_home_ai_events', 'd:' || id) FROM removed;
    DELETE FROM nori_home_ai_pairing_attempts WHERE user_id = account_id;
END;
