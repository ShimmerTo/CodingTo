-- +goose Up
ALTER TABLE tbl_ssh_config ADD COLUMN policy_preset TEXT NOT NULL DEFAULT 'safe';

CREATE TABLE tbl_ssh_policy_override (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    ssh_id          TEXT NOT NULL,
    override_id     TEXT NOT NULL,
    capability      TEXT NOT NULL,
    effect          TEXT NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    position        INTEGER NOT NULL DEFAULT 0,
    create_time     INTEGER NOT NULL DEFAULT 0,
    update_time     INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_ssh_policy_override_key ON tbl_ssh_policy_override(ssh_id, override_id);
CREATE INDEX idx_ssh_policy_override_order ON tbl_ssh_policy_override(ssh_id, position);

CREATE TABLE tbl_ssh_capability (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    ssh_id           TEXT NOT NULL,
    capability_name  TEXT NOT NULL,
    group_name       TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    executable       TEXT NOT NULL,
    args             TEXT NOT NULL DEFAULT '[]',
    params           TEXT NOT NULL DEFAULT '{}',
    permission       TEXT NOT NULL,
    timeout_seconds  INTEGER NOT NULL DEFAULT 30,
    position         INTEGER NOT NULL DEFAULT 0,
    create_time      INTEGER NOT NULL DEFAULT 0,
    update_time      INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_ssh_capability_key ON tbl_ssh_capability(ssh_id, capability_name);
CREATE INDEX idx_ssh_capability_order ON tbl_ssh_capability(ssh_id, position);

-- +goose Down
DROP TABLE IF EXISTS tbl_ssh_capability;
DROP TABLE IF EXISTS tbl_ssh_policy_override;
ALTER TABLE tbl_ssh_config DROP COLUMN policy_preset;
