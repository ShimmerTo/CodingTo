-- +goose Up
-- +goose StatementBegin
-- 用户选择「重置清空」：废弃工作空间与 Git 身份，重构为「环境」
-- （1 个本地目录 + 1 个远程目录，远程目录引用全局 SSH 连接配置）。

DROP TABLE IF EXISTS tbl_git_config;
DROP TABLE IF EXISTS tbl_workspace;

CREATE TABLE IF NOT EXISTS tbl_environment (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    environment_id TEXT NOT NULL DEFAULT '',
    name           TEXT NOT NULL DEFAULT '',
    path           TEXT NOT NULL DEFAULT '',
    description    TEXT NOT NULL DEFAULT '',
    remotes        TEXT NOT NULL DEFAULT '[]',
    active         INTEGER NOT NULL DEFAULT 0,
    create_time    INTEGER NOT NULL DEFAULT 0,
    update_time    INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_environment_id ON tbl_environment(environment_id);

ALTER TABLE tbl_setting RENAME COLUMN last_workspace TO last_environment;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tbl_environment;
-- +goose StatementEnd
