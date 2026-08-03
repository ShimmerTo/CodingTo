-- +goose Up
ALTER TABLE tbl_setting ADD COLUMN system_notification_enabled INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE tbl_setting DROP COLUMN system_notification_enabled;
