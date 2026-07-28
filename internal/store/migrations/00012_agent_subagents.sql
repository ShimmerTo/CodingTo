-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_agent ADD COLUMN subagents TEXT NOT NULL DEFAULT '[]';
ALTER TABLE tbl_agent ADD COLUMN pi_tools TEXT NOT NULL DEFAULT '{}';
ALTER TABLE tbl_agent ADD COLUMN default_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_agent ADD COLUMN default_model TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tbl_agent DROP COLUMN default_model;
ALTER TABLE tbl_agent DROP COLUMN default_provider;
ALTER TABLE tbl_agent DROP COLUMN pi_tools;
ALTER TABLE tbl_agent DROP COLUMN subagents;
-- +goose StatementEnd
