-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_model ADD COLUMN base_url TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tbl_model DROP COLUMN base_url;
-- +goose StatementEnd
