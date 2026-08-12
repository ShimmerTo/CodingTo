-- +goose Up
-- 管家控制面持久化：完整授权上下文、串行事件队列与多待办澄清状态。
ALTER TABLE tbl_steward_permission ADD COLUMN thread TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_steward_permission ADD COLUMN body TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_steward_permission ADD COLUMN plan_json TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_steward_permission ADD COLUMN receive_id_type TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_steward_permission ADD COLUMN reply_to_message_id TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS tbl_steward_event (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    kind                TEXT NOT NULL,
    session_id          INTEGER NOT NULL DEFAULT 0,
    request_id          TEXT NOT NULL DEFAULT '',
    channel_id          INTEGER NOT NULL,
    sender              TEXT NOT NULL DEFAULT '',
    thread              TEXT NOT NULL DEFAULT '',
    receive_id_type     TEXT NOT NULL DEFAULT '',
    reply_to_message_id TEXT NOT NULL DEFAULT '',
    prompt_text         TEXT NOT NULL,
    fallback_text       TEXT NOT NULL DEFAULT '',
    priority            INTEGER NOT NULL DEFAULT 0,
    status              TEXT NOT NULL DEFAULT 'queued',
    last_error          TEXT NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    processed_at        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_steward_event_dispatch
    ON tbl_steward_event(status, priority DESC, created_at, id);

CREATE TABLE IF NOT EXISTS tbl_steward_dialog_state (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    context_key     TEXT NOT NULL UNIQUE,
    channel_id      INTEGER NOT NULL,
    sender          TEXT NOT NULL DEFAULT '',
    thread          TEXT NOT NULL DEFAULT '',
    intent          TEXT NOT NULL,
    candidates_json TEXT NOT NULL,
    created_at      INTEGER NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS tbl_steward_dialog_state;
DROP TABLE IF EXISTS tbl_steward_event;
ALTER TABLE tbl_steward_permission DROP COLUMN reply_to_message_id;
ALTER TABLE tbl_steward_permission DROP COLUMN receive_id_type;
ALTER TABLE tbl_steward_permission DROP COLUMN plan_json;
ALTER TABLE tbl_steward_permission DROP COLUMN body;
ALTER TABLE tbl_steward_permission DROP COLUMN thread;
