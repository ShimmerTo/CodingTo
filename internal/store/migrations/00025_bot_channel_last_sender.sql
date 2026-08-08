-- +goose Up
-- 管家渠道记录每次收到消息的发送者与可发送目标，支持重启后继续测试/主动发送
ALTER TABLE tbl_bot_channel ADD COLUMN last_sender_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_bot_channel ADD COLUMN last_thread_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_bot_channel ADD COLUMN last_receive_id_type TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_bot_channel ADD COLUMN last_message_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_bot_channel ADD COLUMN last_received_at INTEGER NOT NULL DEFAULT 0;

-- +goose Down
-- SQLite 不支持安全地删除单列；回滚时保留新增列，避免破坏已有渠道数据。
