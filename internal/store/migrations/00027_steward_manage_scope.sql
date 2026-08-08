-- +goose Up
-- 接管范围：用枚举 manage_scope 取代旧的布尔 manage_all_sessions
--   值：'all'    = 接管所有非管家自身的会话
--       'butler' = 仅接管管家创建/继续的会话（含机器人派发的任务）
ALTER TABLE tbl_steward_profile ADD COLUMN manage_scope TEXT NOT NULL DEFAULT 'butler';
UPDATE tbl_steward_profile SET manage_scope = 'all' WHERE manage_all_sessions != 0;

-- +goose Down
ALTER TABLE tbl_steward_profile DROP COLUMN manage_scope;
