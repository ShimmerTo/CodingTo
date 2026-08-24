-- +goose Up
ALTER TABLE tbl_ssh_config ADD COLUMN host_key_fingerprint TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE tbl_ssh_config DROP COLUMN host_key_fingerprint;
