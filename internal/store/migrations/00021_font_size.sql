-- +goose Up
ALTER TABLE tbl_setting ADD COLUMN font_size TEXT NOT NULL DEFAULT 'small';

-- +goose Down
ALTER TABLE tbl_setting DROP COLUMN font_size;
