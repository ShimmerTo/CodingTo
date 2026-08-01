-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_agent ADD COLUMN forced_prompt_models TEXT NOT NULL DEFAULT '{}';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tbl_agent DROP COLUMN forced_prompt_models;
-- +goose StatementEnd
