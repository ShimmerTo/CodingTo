-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_ssh_config ADD COLUMN auth_mode TEXT NOT NULL DEFAULT 'password';
ALTER TABLE tbl_ssh_config ADD COLUMN private_key_passphrase TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tbl_ssh_config DROP COLUMN private_key_passphrase;
ALTER TABLE tbl_ssh_config DROP COLUMN auth_mode;
-- +goose StatementEnd