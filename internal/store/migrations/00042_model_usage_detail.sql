-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_setting ADD COLUMN record_api_details INTEGER NOT NULL DEFAULT 0;

ALTER TABLE tbl_token_usage ADD COLUMN request_key TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_token_usage ADD COLUMN api TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_token_usage ADD COLUMN stop_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_token_usage ADD COLUMN success INTEGER NOT NULL DEFAULT 1;

-- Legacy rows intentionally keep an empty request_key. New rows use a stable
-- non-empty key so replay/recovery cannot double-count one provider response.
CREATE UNIQUE INDEX IF NOT EXISTS idx_token_usage_request_key
    ON tbl_token_usage(request_key) WHERE request_key <> '';
CREATE INDEX IF NOT EXISTS idx_token_usage_day_model
    ON tbl_token_usage(day, provider, model);
CREATE INDEX IF NOT EXISTS idx_token_usage_create_time
    ON tbl_token_usage(create_time);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_token_usage_create_time;
DROP INDEX IF EXISTS idx_token_usage_day_model;
DROP INDEX IF EXISTS idx_token_usage_request_key;
ALTER TABLE tbl_token_usage DROP COLUMN success;
ALTER TABLE tbl_token_usage DROP COLUMN stop_reason;
ALTER TABLE tbl_token_usage DROP COLUMN api;
ALTER TABLE tbl_token_usage DROP COLUMN request_key;
ALTER TABLE tbl_setting DROP COLUMN record_api_details;
-- +goose StatementEnd
