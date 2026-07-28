-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS tbl_ssh_config (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ssh_id       TEXT NOT NULL DEFAULT '',
    name         TEXT NOT NULL DEFAULT '',
    private_key  TEXT NOT NULL DEFAULT '',
    public_key   TEXT NOT NULL DEFAULT '',
    passphrase   TEXT NOT NULL DEFAULT '',
    remark       TEXT NOT NULL DEFAULT '',
    create_time  INTEGER NOT NULL DEFAULT 0,
    update_time  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS tbl_git_config (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    git_id        TEXT NOT NULL DEFAULT '',
    name          TEXT NOT NULL DEFAULT '',
    user_name     TEXT NOT NULL DEFAULT '',
    user_email    TEXT NOT NULL DEFAULT '',
    default_branch TEXT NOT NULL DEFAULT '',
    ssh_config_id TEXT NOT NULL DEFAULT '',
    remark        TEXT NOT NULL DEFAULT '',
    create_time   INTEGER NOT NULL DEFAULT 0,
    update_time   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS tbl_workspace (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id   TEXT NOT NULL DEFAULT '',
    name           TEXT NOT NULL DEFAULT '',
    path           TEXT NOT NULL DEFAULT '',
    git_config_id  TEXT NOT NULL DEFAULT '',
    ssh_config_id  TEXT NOT NULL DEFAULT '',
    description    TEXT NOT NULL DEFAULT '',
    active         INTEGER NOT NULL DEFAULT 0,
    create_time    INTEGER NOT NULL DEFAULT 0,
    update_time    INTEGER NOT NULL DEFAULT 0
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tbl_workspace;
DROP TABLE IF EXISTS tbl_git_config;
DROP TABLE IF EXISTS tbl_ssh_config;
-- +goose StatementEnd
