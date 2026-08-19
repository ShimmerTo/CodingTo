-- +goose Up
ALTER TABLE tbl_setting ADD COLUMN session_cleanup_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tbl_setting ADD COLUMN session_cleanup_days INTEGER NOT NULL DEFAULT 14;
-- 把此前按旧默认 30 持久化的行统一改为新默认 14（功能尚未发布，无显式覆盖场景）。
UPDATE tbl_setting SET session_cleanup_days = 14 WHERE session_cleanup_days = 30;

-- +goose Down
ALTER TABLE tbl_setting DROP COLUMN session_cleanup_days;
ALTER TABLE tbl_setting DROP COLUMN session_cleanup_enabled;