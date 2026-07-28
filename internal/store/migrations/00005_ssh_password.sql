-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_ssh_config ADD COLUMN address TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_ssh_config ADD COLUMN username TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_ssh_config ADD COLUMN password TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tbl_ssh_config DROP COLUMN password;
ALTER TABLE tbl_ssh_config DROP COLUMN username;
ALTER TABLE tbl_ssh_config DROP COLUMN address;
-- +goose StatementEnd
