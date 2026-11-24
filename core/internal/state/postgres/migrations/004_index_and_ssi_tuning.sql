-- Historical migration ordering note (#60):
-- 002_indexes.sql and 002_sequences.sql both carry the 002_ prefix and execute in
-- lexicographical order (002_indexes.sql before 002_sequences.sql) via embed.FS.ReadDir.
-- Do NOT rename either file because existing databases record applied filenames in nori_migrations.

-- 1. Migrate nori_records to a tenant-sharded partitioned table with STORED generated columns
-- for all 12 indexed JSONB fields (#46, #60):
--   - Drops unused/redundant indexes (#60 + #46): nori_users_email (data->>'email', unused),
--     nori_records_attention and nori_records_message_sequence (never queried; previously
--     caused 100k+ fallback scans inside SERIALIZABLE txs), and nori_records_index_gin.
--   - Dedicated nori_records_system partition isolates ('system','control') — read inside
--     nearly every SERIALIZABLE tx — onto its own 1-row heap/B-tree so it NEVER shares a
--     leaf page with any user write, even on a brand-new cold DB.
--   - Dedicated nori_records_users partition scopes the unique email index strictly to users.
--   - 32-way HASH(shard) subpartitioning of nori_records_owners co-locates all collections
--     for one user (shard = owner ID) inside a single partition, eliminating cross-user
--     multi-partition SIREAD rw-conflict cycles from row #1 on cold and warm DBs alike.
--   - Moving all 12 indexed JSONB fields to STORED generated columns removes raw `data` from
--     Postgres's HOT-blocking column bitmap; combined with fillfactor=70, non-indexed JSONB
--     updates execute as pure HOT updates with zero index writes and zero leaf-page SIREAD
--     conflicts even between concurrent transactions for the same user.
--   - Splits run_id partial index for results vs task_commands so Publish (reading results)
--     never conflicts on run_id index pages with AcceptCommandBatch (inserting task_commands).
DO $$
DECLARE
    relkind "char";
    i int;
BEGIN
    SELECT c.relkind INTO relkind
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = current_schema() AND c.relname = 'nori_records';

    IF relkind = 'r' THEN
        DROP INDEX IF EXISTS nori_users_email;
        DROP INDEX IF EXISTS nori_users_email_jsonb;
        DROP INDEX IF EXISTS nori_records_attention;
        DROP INDEX IF EXISTS nori_records_message_sequence;
        DROP INDEX IF EXISTS nori_records_index_gin;
        DROP INDEX IF EXISTS nori_records_sequence;
        DROP INDEX IF EXISTS nori_records_created;
        DROP INDEX IF EXISTS nori_records_started;
        DROP INDEX IF EXISTS nori_records_position;
        DROP INDEX IF EXISTS nori_records_status;
        DROP INDEX IF EXISTS nori_records_run_id;
        DROP INDEX IF EXISTS nori_records_index_status;
        DROP INDEX IF EXISTS nori_records_index_task_id;
        DROP INDEX IF EXISTS nori_records_undelivered;
        DROP INDEX IF EXISTS nori_records_cleanup_pending;
        DROP INDEX IF EXISTS nori_records_publication_pending;

        ALTER TABLE nori_records RENAME TO nori_records_legacy;
        ALTER INDEX IF EXISTS nori_records_pkey RENAME TO nori_records_legacy_pkey;

        CREATE TABLE nori_records (
            shard                   text  NOT NULL,
            collection              text  NOT NULL,
            id                      text  NOT NULL,
            data                    jsonb NOT NULL CHECK (jsonb_typeof(data) = 'object'),
            idx_seq                 jsonb GENERATED ALWAYS AS (data->'seq') STORED,
            idx_created_at          jsonb GENERATED ALWAYS AS (data->'created_at') STORED,
            idx_started_at          jsonb GENERATED ALWAYS AS (data->'started_at') STORED,
            idx_position            jsonb GENERATED ALWAYS AS (data->'position') STORED,
            idx_status              jsonb GENERATED ALWAYS AS (data->'status') STORED,
            idx_run_id              jsonb GENERATED ALWAYS AS (data->'run_id') STORED,
            idx_delivered           jsonb GENERATED ALWAYS AS (data->'delivered') STORED,
            idx_cleanup_pending     jsonb GENERATED ALWAYS AS (data->'cleanup_pending') STORED,
            idx_publication_pending jsonb GENERATED ALWAYS AS (data->'publication_pending') STORED,
            idx_email               jsonb GENERATED ALWAYS AS (data->'email') STORED,
            idx_index_status        jsonb GENERATED ALWAYS AS (data#>'{index,status}') STORED,
            idx_index_task_id       jsonb GENERATED ALWAYS AS (data#>'{index,task_id}') STORED,
            PRIMARY KEY (shard, collection, id)
        ) PARTITION BY LIST (shard);

        CREATE TABLE nori_records_system PARTITION OF nori_records
            FOR VALUES IN ('system')
            WITH (fillfactor = 50, autovacuum_analyze_scale_factor = 0.01, autovacuum_analyze_threshold = 5);

        CREATE TABLE nori_records_users PARTITION OF nori_records
            FOR VALUES IN ('users')
            WITH (fillfactor = 70, autovacuum_analyze_scale_factor = 0.01, autovacuum_analyze_threshold = 10);

        CREATE TABLE nori_records_owners PARTITION OF nori_records
            DEFAULT
            PARTITION BY HASH (shard);

        FOR i IN 0..31 LOOP
            EXECUTE format(
                'CREATE TABLE nori_records_p%s PARTITION OF nori_records_owners FOR VALUES WITH (MODULUS 32, REMAINDER %s) WITH (fillfactor = 60, autovacuum_analyze_scale_factor = 0.01, autovacuum_analyze_threshold = 15, autovacuum_vacuum_scale_factor = 0.05, autovacuum_vacuum_threshold = 25)',
                lpad(i::text, 2, '0'), i
            );
        END LOOP;

        INSERT INTO nori_records (shard, collection, id, data)
        SELECT
            CASE
                WHEN collection LIKE 'owners/%/%' THEN 'owners/' || split_part(collection, '/', 2)
                ELSE collection
            END,
            collection,
            id,
            data
        FROM nori_records_legacy;

        DROP TABLE nori_records_legacy;

        -- Unique email index lives strictly on nori_records_users (0 overhead on owners/system).
        CREATE UNIQUE INDEX nori_users_email_jsonb
            ON nori_records_users (idx_email)
            WHERE idx_email IS NOT NULL;

        -- Owner-scoped secondary indexes live strictly on nori_records_owners (0 overhead on system/users).
        CREATE INDEX nori_records_sequence
            ON nori_records_owners (shard, collection, idx_seq, id)
            WHERE idx_seq IS NOT NULL;

        CREATE INDEX nori_records_created
            ON nori_records_owners (shard, collection, idx_created_at, id)
            WHERE idx_created_at IS NOT NULL;

        CREATE INDEX nori_records_started
            ON nori_records_owners (shard, collection, idx_started_at, id)
            WHERE idx_started_at IS NOT NULL;

        CREATE INDEX nori_records_position
            ON nori_records_owners (shard, collection, idx_position, id)
            WHERE idx_position IS NOT NULL;

        CREATE INDEX nori_records_status
            ON nori_records_owners (shard, collection, id)
            WHERE idx_status = '"running"'::jsonb;

        CREATE INDEX nori_records_run_id
            ON nori_records_owners (shard, collection, idx_run_id, id)
            WHERE idx_run_id IS NOT NULL AND right(collection, 8) <> '/results';

        CREATE INDEX nori_records_results_run_id
            ON nori_records_owners (shard, collection, idx_run_id, id)
            WHERE idx_run_id IS NOT NULL AND right(collection, 8) = '/results';

        CREATE INDEX nori_records_index_status
            ON nori_records_owners (shard, collection, idx_index_status, id)
            WHERE idx_index_status IS NOT NULL;

        CREATE INDEX nori_records_index_task_id
            ON nori_records_owners (shard, collection, idx_index_task_id, id)
            WHERE idx_index_task_id IS NOT NULL;

        CREATE INDEX nori_records_undelivered
            ON nori_records_owners (shard, collection, id)
            WHERE idx_delivered = 'false'::jsonb;

        CREATE INDEX nori_records_cleanup_pending
            ON nori_records_owners (shard, collection, id)
            WHERE idx_cleanup_pending = 'true'::jsonb;

        CREATE INDEX nori_records_publication_pending
            ON nori_records_owners (shard, collection, id)
            WHERE idx_publication_pending = 'true'::jsonb;
    END IF;
END $$;

-- 2. Hash-partition nori_sequences into 32 physical partitions (#46).
-- Moves per-user event sequence rows off a single shared heap/index page to eliminate
-- cross-user SSI false conflicts. fillfactor=50 keeps seq increments as HOT updates.
DO $$
DECLARE
    relkind "char";
    i int;
BEGIN
    SELECT c.relkind INTO relkind
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = current_schema() AND c.relname = 'nori_sequences';

    IF relkind = 'r' THEN
        ALTER TABLE nori_sequences RENAME TO nori_sequences_legacy;
        ALTER INDEX IF EXISTS nori_sequences_pkey RENAME TO nori_sequences_legacy_pkey;

        CREATE TABLE nori_sequences (
            path text PRIMARY KEY,
            seq bigint NOT NULL
        ) PARTITION BY HASH (path);

        FOR i IN 0..31 LOOP
            EXECUTE format(
                'CREATE TABLE nori_sequences_p%s PARTITION OF nori_sequences FOR VALUES WITH (MODULUS 32, REMAINDER %s) WITH (fillfactor = 50, autovacuum_analyze_scale_factor = 0.01, autovacuum_analyze_threshold = 5)',
                lpad(i::text, 2, '0'), i
            );
        END LOOP;

        INSERT INTO nori_sequences (path, seq)
        SELECT path, seq FROM nori_sequences_legacy;

        DROP TABLE nori_sequences_legacy;
    END IF;
END $$;

-- 3. HOT headroom + aggressive autovacuum/ANALYZE thresholds on high-churn queue tables (#46).
ALTER TABLE nori_task_leases SET (
    fillfactor = 70,
    autovacuum_analyze_scale_factor = 0.01,
    autovacuum_analyze_threshold = 10,
    autovacuum_vacuum_scale_factor = 0.02,
    autovacuum_vacuum_threshold = 20
);

ALTER TABLE nori_user_wakes SET (
    fillfactor = 70,
    autovacuum_analyze_scale_factor = 0.01,
    autovacuum_analyze_threshold = 10,
    autovacuum_vacuum_scale_factor = 0.02,
    autovacuum_vacuum_threshold = 20
);
