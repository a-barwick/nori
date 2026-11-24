CREATE UNIQUE INDEX IF NOT EXISTS nori_users_email_jsonb ON nori_records ((data->'email')) WHERE collection = 'users';
CREATE INDEX IF NOT EXISTS nori_records_undelivered ON nori_records (collection, id) WHERE (data->'delivered') = 'false'::jsonb;
CREATE INDEX IF NOT EXISTS nori_records_cleanup_pending ON nori_records (collection, id) WHERE (data->'cleanup_pending') = 'true'::jsonb;
CREATE INDEX IF NOT EXISTS nori_records_publication_pending ON nori_records (collection, id) WHERE (data->'publication_pending') = 'true'::jsonb;
CREATE INDEX IF NOT EXISTS nori_records_run_id ON nori_records (collection, (data->'run_id'), id) WHERE data ? 'run_id';
CREATE INDEX IF NOT EXISTS nori_records_index_status ON nori_records (collection, (data#>'{index,status}'), id) WHERE data ? 'index';
CREATE INDEX IF NOT EXISTS nori_records_index_task_id ON nori_records (collection, (data#>'{index,task_id}'), id) WHERE data ? 'index';
CREATE INDEX IF NOT EXISTS nori_records_index_gin ON nori_records USING gin ((data->'index')) WHERE data ? 'index';
