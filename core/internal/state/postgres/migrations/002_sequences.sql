CREATE TABLE IF NOT EXISTS nori_sequences (
    path text PRIMARY KEY,
    seq bigint NOT NULL
);

INSERT INTO nori_sequences(path, seq)
SELECT collection || '/' || id, COALESCE((data->>'seq')::bigint, 0)
FROM nori_records
WHERE collection LIKE 'owners/%/event_head' AND id = 'head'
ON CONFLICT (path) DO UPDATE SET seq = GREATEST(nori_sequences.seq, EXCLUDED.seq);
