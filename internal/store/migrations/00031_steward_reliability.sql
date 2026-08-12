-- +goose Up
-- 管家事件可靠性：持久化派发令牌/尝试次数/租约，并保证一个会话只绑定一个机器人任务。
ALTER TABLE tbl_steward_event ADD COLUMN dispatch_token TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_steward_event ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tbl_steward_event ADD COLUMN lease_until INTEGER NOT NULL DEFAULT 0;

-- 历史库可能已经存在重复 session_id。优先保留仍在运行的最新任务，
-- 否则保留最新记录，再建立唯一索引，避免重启时旧记录覆盖新渠道绑定。
DELETE FROM tbl_bot_task
WHERE id NOT IN (
    SELECT id FROM (
        SELECT id,
               ROW_NUMBER() OVER (
                   PARTITION BY session_id
                   ORDER BY CASE WHEN status IN ('pending', 'running') THEN 0 ELSE 1 END, id DESC
               ) AS row_num
        FROM tbl_bot_task
    ) ranked
    WHERE row_num = 1
);
DROP INDEX IF EXISTS idx_bot_task_session;
CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_task_session_unique ON tbl_bot_task(session_id);

CREATE INDEX IF NOT EXISTS idx_steward_permission_retention
    ON tbl_steward_permission(status, answered_at);
CREATE INDEX IF NOT EXISTS idx_steward_event_retention
    ON tbl_steward_event(status, processed_at);

CREATE TABLE IF NOT EXISTS tbl_steward_inbound_dedup (
    channel_id INTEGER NOT NULL,
    message_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (channel_id, message_id)
);
CREATE INDEX IF NOT EXISTS idx_steward_inbound_dedup_retention
    ON tbl_steward_inbound_dedup(created_at);

-- +goose Down
DROP TABLE IF EXISTS tbl_steward_inbound_dedup;
DROP INDEX IF EXISTS idx_steward_event_retention;
DROP INDEX IF EXISTS idx_steward_permission_retention;
DROP INDEX IF EXISTS idx_bot_task_session_unique;
CREATE INDEX IF NOT EXISTS idx_bot_task_session ON tbl_bot_task(session_id);
-- SQLite 的兼容回滚保留新增列，避免重建表造成数据损失。
