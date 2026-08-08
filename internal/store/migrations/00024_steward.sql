-- +goose Up
-- 管家（Steward）机器人接入：渠道 / 人设 / 任务 / 授权请求
CREATE TABLE IF NOT EXISTS tbl_bot_channel (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    platform    TEXT NOT NULL,
    name        TEXT NOT NULL,
    mode        TEXT NOT NULL,
    config_json TEXT NOT NULL DEFAULT '{}',
    enabled     INTEGER NOT NULL DEFAULT 0,
    status      TEXT NOT NULL DEFAULT 'disconnected',
    last_error  TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS tbl_steward_profile (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id             TEXT NOT NULL DEFAULT '',
    role                 TEXT NOT NULL DEFAULT '',
    tone                 TEXT NOT NULL DEFAULT '',
    prompt               TEXT NOT NULL DEFAULT '',
    provider             TEXT NOT NULL DEFAULT '',
    model                TEXT NOT NULL DEFAULT '',
    idle_timeout_min     INTEGER NOT NULL DEFAULT 10,
    resident_always      INTEGER NOT NULL DEFAULT 0,
    manage_all_sessions  INTEGER NOT NULL DEFAULT 0,
    enabled              INTEGER NOT NULL DEFAULT 1,
    updated_at           INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS tbl_bot_task (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  INTEGER NOT NULL,
    channel_id  INTEGER NOT NULL,
    sender      TEXT NOT NULL,
    thread      TEXT,
    status      TEXT NOT NULL DEFAULT 'pending',
    task_brief  TEXT,
    result_text TEXT,
    created_at  INTEGER NOT NULL,
    finished_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_bot_task_session ON tbl_bot_task(session_id);
CREATE INDEX IF NOT EXISTS idx_bot_task_channel_status ON tbl_bot_task(channel_id, status);

CREATE TABLE IF NOT EXISTS tbl_steward_permission (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id   TEXT NOT NULL UNIQUE,
    session_id   INTEGER NOT NULL,
    run_id       TEXT,
    channel_id   INTEGER NOT NULL,
    sender       TEXT NOT NULL,
    method       TEXT NOT NULL,
    title        TEXT NOT NULL,
    options_json TEXT,
    scope        TEXT NOT NULL DEFAULT 'once',
    status       TEXT NOT NULL DEFAULT 'pending',
    answer       TEXT,
    created_at   INTEGER NOT NULL,
    answered_at  INTEGER
);
CREATE INDEX IF NOT EXISTS idx_steward_perm_status ON tbl_steward_permission(status);

-- +goose Down
DROP TABLE IF EXISTS tbl_steward_permission;
DROP TABLE IF EXISTS tbl_bot_task;
DROP TABLE IF EXISTS tbl_steward_profile;
DROP TABLE IF EXISTS tbl_bot_channel;
