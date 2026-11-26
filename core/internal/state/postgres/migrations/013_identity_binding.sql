CREATE UNIQUE INDEX IF NOT EXISTS nori_users_identity_uidx
    ON nori_records_users ((data->'identity_issuer'), (data->'identity_subject'))
    WHERE collection = 'users'
      AND data->'identity_issuer' IS NOT NULL
      AND data->'identity_subject' IS NOT NULL;
