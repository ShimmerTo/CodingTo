-- +goose Up
ALTER TABLE tbl_setting ADD COLUMN concise_chat INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE tbl_setting DROP COLUMN concise_chat;
