-- Dedicated 32-way hash-partitioned full-text search index for Layer 1 (Core)
-- and Layer 2 (Daily Log) memory chunks. Kept separate from nori_records so
-- 004_index_and_ssi_tuning.sql's HOT-update bitmap and zero-SIREAD-escalation
-- properties on nori_records remain completely untouched.

CREATE TABLE IF NOT EXISTS nori_memory_chunks (
    owner          text        NOT NULL,
    chunk_id       text        NOT NULL,
    layer          text        NOT NULL CHECK (layer IN ('core', 'log')),
    log_date       date,
    section        text        NOT NULL DEFAULT '',
    about          text        NOT NULL DEFAULT '',
    body           text        NOT NULL,
    provenance     text        NOT NULL,
    source_input   text        NOT NULL DEFAULT '',
    run_id         text        NOT NULL DEFAULT '',
    pinned         boolean     NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    search_tsv     tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(section, '') || ' ' || coalesce(about, '')), 'A') ||
        setweight(to_tsvector('english', body), 'B') ||
        setweight(to_tsvector('simple', coalesce(section, '') || ' ' || coalesce(about, '') || ' ' || body), 'B')
    ) STORED,
    PRIMARY KEY (owner, chunk_id)
) PARTITION BY HASH (owner);

DO $$
DECLARE
    i int;
BEGIN
    FOR i IN 0..31 LOOP
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS nori_memory_chunks_p%s PARTITION OF nori_memory_chunks FOR VALUES WITH (MODULUS 32, REMAINDER %s) WITH (fillfactor = 80, autovacuum_analyze_scale_factor = 0.02, autovacuum_analyze_threshold = 10)',
            lpad(i::text, 2, '0'), i
        );
    END LOOP;
END $$;

CREATE INDEX IF NOT EXISTS nori_memory_chunks_tsv_gin
    ON nori_memory_chunks USING GIN (search_tsv);

CREATE INDEX IF NOT EXISTS nori_memory_chunks_owner_updated
    ON nori_memory_chunks (owner, layer, updated_at DESC);

-- Idempotent backfill of existing Layer 1 Core memories stored in nori_records.
INSERT INTO nori_memory_chunks (
    owner,
    chunk_id,
    layer,
    log_date,
    section,
    about,
    body,
    provenance,
    source_input,
    run_id,
    pinned,
    created_at,
    updated_at
)
SELECT
    split_part(collection, '/', 2) AS owner,
    'core:' || COALESCE(NULLIF(data->>'id', ''), id) AS chunk_id,
    'core' AS layer,
    NULL::date AS log_date,
    COALESCE(data->>'section', '') AS section,
    COALESCE(data->>'about', '') AS about,
    COALESCE(data->>'text', '') AS body,
    COALESCE(NULLIF(data->>'provenance', ''), 'user_explicit') AS provenance,
    COALESCE(data->>'source_input_id', '') AS source_input,
    '' AS run_id,
    COALESCE((data->>'pinned')::boolean, false) AS pinned,
    COALESCE(NULLIF(data->>'created_at', '')::timestamptz, now()) AS created_at,
    COALESCE(NULLIF(data->>'updated_at', '')::timestamptz, NULLIF(data->>'created_at', '')::timestamptz, now()) AS updated_at
FROM nori_records
WHERE right(collection, 9) = '/memories'
  AND collection LIKE 'owners/%/memories'
  AND split_part(collection, '/', 2) <> ''
ON CONFLICT (owner, chunk_id) DO NOTHING;
