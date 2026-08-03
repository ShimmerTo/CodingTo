-- +goose Up
ALTER TABLE tbl_setting ADD COLUMN subagent_concurrency INTEGER NOT NULL DEFAULT 4;

-- +goose Down
ALTER TABLE tbl_setting DROP COLUMN subagent_concurrency;
