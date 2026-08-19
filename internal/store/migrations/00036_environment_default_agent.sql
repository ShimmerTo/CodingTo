-- +goose Up
ALTER TABLE tbl_environment ADD COLUMN default_agent_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE tbl_environment DROP COLUMN default_agent_id;
