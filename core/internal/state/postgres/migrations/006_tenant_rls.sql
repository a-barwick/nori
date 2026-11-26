-- Tenant Row-Level Security (RLS) for multi-tenant isolation across
-- nori_records, nori_task_leases, and nori_user_wakes.
--
-- owner_id is a STORED generated column on all three tables so Postgres
-- populates and maintains it automatically without manual backfills or
-- rolling-deploy drift from older binaries.

ALTER TABLE nori_records
    ADD COLUMN IF NOT EXISTS owner_id text GENERATED ALWAYS AS (
        CASE WHEN collection LIKE 'owners/%' THEN split_part(collection, '/', 2) ELSE '' END
    ) STORED;

ALTER TABLE nori_task_leases
    ADD COLUMN IF NOT EXISTS owner_id text GENERATED ALWAYS AS (user_id) STORED;

ALTER TABLE nori_user_wakes
    ADD COLUMN IF NOT EXISTS owner_id text GENERATED ALWAYS AS (user_id) STORED;

-- Tenant lookup indexes (nori_records uses a partial index excluding shared system rows).
CREATE INDEX IF NOT EXISTS nori_records_owner_id
    ON nori_records (owner_id)
    WHERE owner_id <> '';

CREATE INDEX IF NOT EXISTS nori_task_leases_owner_id
    ON nori_task_leases (owner_id);

CREATE INDEX IF NOT EXISTS nori_user_wakes_owner_id
    ON nori_user_wakes (owner_id);

-- Enable and FORCE Row-Level Security with idempotent policies so Postgres
-- enforces tenant isolation even when connecting as the table owner.
-- NULLIF(current_setting('app.current_owner', true), '') IS NULL permits
-- unscoped system/worker discovery, while user-scoped transactions set
-- app.current_owner and can only access their own tenant rows plus shared
-- system rows (owner_id = '').
ALTER TABLE nori_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE nori_records FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS nori_records_tenant_isolation ON nori_records;
CREATE POLICY nori_records_tenant_isolation ON nori_records
    USING (
        NULLIF(current_setting('app.current_owner', true), '') IS NULL
        OR owner_id = ''
        OR owner_id = current_setting('app.current_owner', true)
    )
    WITH CHECK (
        NULLIF(current_setting('app.current_owner', true), '') IS NULL
        OR owner_id = ''
        OR owner_id = current_setting('app.current_owner', true)
    );

ALTER TABLE nori_task_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE nori_task_leases FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS nori_task_leases_tenant_isolation ON nori_task_leases;
CREATE POLICY nori_task_leases_tenant_isolation ON nori_task_leases
    USING (
        NULLIF(current_setting('app.current_owner', true), '') IS NULL
        OR owner_id = ''
        OR owner_id = current_setting('app.current_owner', true)
    )
    WITH CHECK (
        NULLIF(current_setting('app.current_owner', true), '') IS NULL
        OR owner_id = ''
        OR owner_id = current_setting('app.current_owner', true)
    );

ALTER TABLE nori_user_wakes ENABLE ROW LEVEL SECURITY;
ALTER TABLE nori_user_wakes FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS nori_user_wakes_tenant_isolation ON nori_user_wakes;
CREATE POLICY nori_user_wakes_tenant_isolation ON nori_user_wakes
    USING (
        NULLIF(current_setting('app.current_owner', true), '') IS NULL
        OR owner_id = ''
        OR owner_id = current_setting('app.current_owner', true)
    )
    WITH CHECK (
        NULLIF(current_setting('app.current_owner', true), '') IS NULL
        OR owner_id = ''
        OR owner_id = current_setting('app.current_owner', true)
    );
