-- +goose Up
ALTER TABLE tbl_setting ADD COLUMN diff_mode TEXT NOT NULL DEFAULT 'unified';

-- +goose Down
ALTER TABLE tbl_setting DROP COLUMN diff_mode;
