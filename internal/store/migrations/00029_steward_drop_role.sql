-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_steward_profile DROP COLUMN role;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tbl_steward_profile ADD COLUMN role TEXT NOT NULL DEFAULT '全能型 AI 助手与任务管家';
-- +goose StatementEnd
