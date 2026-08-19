-- +goose Up
ALTER TABLE tbl_setting ADD COLUMN dcg_policy TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE tbl_setting DROP COLUMN dcg_policy;
