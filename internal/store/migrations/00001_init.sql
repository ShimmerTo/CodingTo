-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS tbl_setting (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    theme           TEXT NOT NULL DEFAULT 'system',
    language        TEXT NOT NULL DEFAULT 'zh-CN',
    default_provider TEXT NOT NULL DEFAULT '',
    default_model   TEXT NOT NULL DEFAULT '',
    last_workspace  TEXT NOT NULL DEFAULT '',
    plan_mode_enabled INTEGER NOT NULL DEFAULT 1,
    headroom        TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS tbl_provider (
    name        TEXT PRIMARY KEY,
    label       TEXT NOT NULL DEFAULT '',
    vendor      TEXT NOT NULL DEFAULT '',
    api         TEXT NOT NULL DEFAULT '',
    base_url    TEXT NOT NULL DEFAULT '',
    api_key     TEXT NOT NULL DEFAULT '',
    oauth       TEXT NOT NULL DEFAULT '',
    headers     TEXT NOT NULL DEFAULT '{}',
    auth_header INTEGER NOT NULL DEFAULT 0,
    enabled     INTEGER NOT NULL DEFAULT 0,
    compat      TEXT NOT NULL DEFAULT '{}',
    sort_order  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS tbl_model (
    id              TEXT NOT NULL,
    provider_name   TEXT NOT NULL,
    name            TEXT NOT NULL DEFAULT '',
    api             TEXT NOT NULL DEFAULT '',
    reasoning       INTEGER NOT NULL DEFAULT 0,
    thinking_level_map TEXT NOT NULL DEFAULT '{}',
    default_thinking_level TEXT NOT NULL DEFAULT '',
    input           TEXT NOT NULL DEFAULT '[]',
    context_window  INTEGER NOT NULL DEFAULT 0,
    max_tokens      INTEGER NOT NULL DEFAULT 0,
    cost            TEXT NOT NULL DEFAULT '{}',
    capabilities    TEXT NOT NULL DEFAULT '{}',
    compat          TEXT NOT NULL DEFAULT '{}',
    sort_order      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (provider_name, id),
    FOREIGN KEY (provider_name) REFERENCES tbl_provider(name) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS tbl_agent (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id    TEXT NOT NULL DEFAULT '',
    name        TEXT NOT NULL DEFAULT '',
    data_dir    TEXT NOT NULL DEFAULT '' UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    builtin     TEXT NOT NULL DEFAULT '{}',
    recommended TEXT NOT NULL DEFAULT '{}',
    active      INTEGER NOT NULL DEFAULT 0,
    create_time INTEGER NOT NULL DEFAULT 0,
    update_time INTEGER NOT NULL DEFAULT 0
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tbl_agent;
DROP TABLE IF EXISTS tbl_model;
DROP TABLE IF EXISTS tbl_provider;
DROP TABLE IF EXISTS tbl_setting;
-- +goose StatementEnd
