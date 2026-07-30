-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_setting ADD COLUMN user_name TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_setting ADD COLUMN user_avatar TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tbl_setting DROP COLUMN user_avatar;
ALTER TABLE tbl_setting DROP COLUMN user_name;
-- +goose StatementEnd
