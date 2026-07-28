-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_setting ADD COLUMN figma TEXT NOT NULL DEFAULT '{}';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tbl_setting DROP COLUMN figma;
-- +goose StatementEnd
