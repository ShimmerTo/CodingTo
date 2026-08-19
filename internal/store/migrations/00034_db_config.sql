-- +goose Up
ALTER TABLE tbl_setting ADD COLUMN db_config TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_environment ADD COLUMN db_connections TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE tbl_setting DROP COLUMN db_config;
ALTER TABLE tbl_environment DROP COLUMN db_connections;
