-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_agent ADD COLUMN browser_profile_policy TEXT NOT NULL DEFAULT '{}';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tbl_agent DROP COLUMN browser_profile_policy;
-- +goose StatementEnd
