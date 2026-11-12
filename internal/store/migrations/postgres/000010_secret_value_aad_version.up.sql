ALTER TABLE secret_versions
    ADD COLUMN data_aad_version INTEGER NOT NULL DEFAULT 1 CHECK (data_aad_version > 0);

ALTER TABLE secret_versions
    ADD COLUMN data_aad_key_id VARCHAR(128) NOT NULL DEFAULT '';

UPDATE secret_versions
SET data_aad_key_id = key_id
WHERE data_aad_version = 1 AND data_aad_key_id = '';
