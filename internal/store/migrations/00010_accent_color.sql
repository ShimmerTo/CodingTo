-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_setting ADD COLUMN accent_color TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tbl_setting DROP COLUMN accent_color;
-- +goose StatementEnd
