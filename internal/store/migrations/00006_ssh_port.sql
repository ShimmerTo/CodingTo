-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_ssh_config ADD COLUMN port INTEGER NOT NULL DEFAULT 22;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tbl_ssh_config DROP COLUMN port;
-- +goose StatementEnd
