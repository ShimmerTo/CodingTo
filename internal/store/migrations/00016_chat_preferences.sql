-- +goose Up
ALTER TABLE tbl_setting ADD COLUMN chat_layout TEXT NOT NULL DEFAULT 'left';
ALTER TABLE tbl_setting ADD COLUMN show_identity INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE tbl_setting DROP COLUMN chat_layout;
ALTER TABLE tbl_setting DROP COLUMN show_identity;
