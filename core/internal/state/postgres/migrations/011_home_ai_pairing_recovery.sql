ALTER TABLE nori_home_ai_devices ADD COLUMN pairing_hash bytea;
CREATE UNIQUE INDEX nori_home_ai_devices_pairing_hash ON nori_home_ai_devices (pairing_hash) WHERE pairing_hash IS NOT NULL;
