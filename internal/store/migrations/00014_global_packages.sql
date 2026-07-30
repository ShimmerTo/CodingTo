-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_setting ADD COLUMN global_mcp TEXT NOT NULL DEFAULT '[]';
ALTER TABLE tbl_setting ADD COLUMN global_plugins TEXT NOT NULL DEFAULT '[]';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tbl_setting DROP COLUMN global_plugins;
ALTER TABLE tbl_setting DROP COLUMN global_mcp;
-- +goose StatementEnd
