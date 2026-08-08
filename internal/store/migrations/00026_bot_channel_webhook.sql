-- +goose Up
-- 钉钉机器人会话回复地址（SessionWebhook，有效期约 2 小时）持久化，
-- 支持重启/重连后仍可回复与测试发送。
ALTER TABLE tbl_bot_channel ADD COLUMN last_webhook TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_bot_channel ADD COLUMN last_webhook_at INTEGER NOT NULL DEFAULT 0;

-- +goose Down
-- SQLite 不支持安全地删除单列；回滚时保留新增列，避免破坏已有渠道数据。
