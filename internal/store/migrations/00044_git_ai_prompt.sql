-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS tbl_git_ai_prompt (
    prompt_type TEXT PRIMARY KEY CHECK (prompt_type IN ('commit', 'file_analysis')),
    prompt_text TEXT NOT NULL,
    update_time INTEGER NOT NULL DEFAULT 0
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tbl_git_ai_prompt;
-- +goose StatementEnd
