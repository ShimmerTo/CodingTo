-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_agent ADD COLUMN avatar TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tbl_agent DROP COLUMN avatar;
-- +goose StatementEnd
