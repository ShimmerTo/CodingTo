-- +goose Up
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_token_usage_day_session_cursor
    ON tbl_token_usage(day, session_id, create_time DESC, id DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_token_usage_day_session_cursor;
-- +goose StatementEnd
