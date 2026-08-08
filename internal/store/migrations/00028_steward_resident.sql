-- +goose Up
-- 管家常驻能力：常驻会话ID持久化 + 管家名称 + 上下文压缩轮数
--   resident_session_id：常驻对话的会话ID，重启后恢复复用，避免每次启动新建对话
--   name：管家名称（用于人设与上线通知）
--   compact_after_turns：超过该轮数后自动压缩上下文（默认 20）
ALTER TABLE tbl_steward_profile ADD COLUMN resident_session_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tbl_steward_profile ADD COLUMN name TEXT NOT NULL DEFAULT '';
ALTER TABLE tbl_steward_profile ADD COLUMN compact_after_turns INTEGER NOT NULL DEFAULT 20;

-- +goose Down
ALTER TABLE tbl_steward_profile DROP COLUMN resident_session_id;
ALTER TABLE tbl_steward_profile DROP COLUMN name;
ALTER TABLE tbl_steward_profile DROP COLUMN compact_after_turns;
