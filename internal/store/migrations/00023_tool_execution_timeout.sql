-- +goose Up
ALTER TABLE tbl_setting ADD COLUMN tool_execution_timeout INTEGER NOT NULL DEFAULT 10;

-- +goose Down
ALTER TABLE tbl_setting DROP COLUMN tool_execution_timeout;
