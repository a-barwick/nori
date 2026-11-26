CREATE TABLE IF NOT EXISTS nori_records (
    collection text NOT NULL,
    id text NOT NULL,
    data jsonb NOT NULL CHECK (jsonb_typeof(data) = 'object'),
    PRIMARY KEY (collection, id)
);
CREATE INDEX IF NOT EXISTS nori_records_sequence ON nori_records (collection, (data->'seq'));
CREATE INDEX IF NOT EXISTS nori_records_message_sequence ON nori_records (collection, (data->'sequence'));
CREATE INDEX IF NOT EXISTS nori_records_created ON nori_records (collection, (data->'created_at'), id);
CREATE INDEX IF NOT EXISTS nori_records_started ON nori_records (collection, (data->'started_at'), id);
CREATE INDEX IF NOT EXISTS nori_records_position ON nori_records (collection, (data->'position'));
CREATE INDEX IF NOT EXISTS nori_records_status ON nori_records (collection, (data->'status'));
CREATE INDEX IF NOT EXISTS nori_records_attention ON nori_records (collection, (data->'attention'));
CREATE UNIQUE INDEX IF NOT EXISTS nori_users_email ON nori_records ((data->>'email')) WHERE collection = 'users';
INSERT INTO nori_records(collection,id,data) VALUES ('system','control','{"paused":false}') ON CONFLICT DO NOTHING;
