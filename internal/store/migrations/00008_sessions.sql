-- +goose Up
-- +goose StatementBegin
ALTER TABLE tbl_setting ADD COLUMN session_dir TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS tbl_session (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id         TEXT NOT NULL DEFAULT '',
    environment_id   TEXT NOT NULL DEFAULT '',
    title            TEXT NOT NULL DEFAULT '',
    session_dir      TEXT NOT NULL DEFAULT '',
    session_path     TEXT NOT NULL DEFAULT '',
    provider         TEXT NOT NULL DEFAULT '',
    model            TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'active',
    exec_duration_ms INTEGER NOT NULL DEFAULT 0,
    create_time      INTEGER NOT NULL DEFAULT 0,
    update_time      INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_session_environment_update
    ON tbl_session(environment_id, update_time DESC);
CREATE INDEX IF NOT EXISTS idx_session_agent
    ON tbl_session(agent_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tbl_session;
ALTER TABLE tbl_setting DROP COLUMN session_dir;
-- +goose StatementEnd
