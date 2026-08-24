-- +goose Up
-- +goose StatementBegin
-- 记录每次大模型请求返回的 token 消耗（按日、按会话）。由主 Agent 与子 Agent
-- 在请求返回处实时写入，统计接口直接按 day / session_id 聚合，避免事后扫描会话文件。
CREATE TABLE IF NOT EXISTS tbl_token_usage (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    day         TEXT    NOT NULL DEFAULT '',
    session_id  INTEGER NOT NULL DEFAULT 0,
    agent_id    TEXT    NOT NULL DEFAULT '',
    provider    TEXT    NOT NULL DEFAULT '',
    model       TEXT    NOT NULL DEFAULT '',
    input       INTEGER NOT NULL DEFAULT 0,
    cache_read  INTEGER NOT NULL DEFAULT 0,
    cache_write INTEGER NOT NULL DEFAULT 0,
    output      INTEGER NOT NULL DEFAULT 0,
    total       INTEGER NOT NULL DEFAULT 0,
    create_time INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_token_usage_day_session
    ON tbl_token_usage(day, session_id);
CREATE INDEX IF NOT EXISTS idx_token_usage_session
    ON tbl_token_usage(session_id);
CREATE INDEX IF NOT EXISTS idx_token_usage_day
    ON tbl_token_usage(day);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tbl_token_usage;
-- +goose StatementEnd
