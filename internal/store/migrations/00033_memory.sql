-- +goose Up
ALTER TABLE tbl_setting ADD COLUMN project_history_limit INTEGER NOT NULL DEFAULT 100;
UPDATE tbl_agent
SET builtin = CASE
    WHEN builtin = '{}' THEN '{"memory":true}'
    ELSE substr(rtrim(builtin), 1, length(rtrim(builtin)) - 1) || ',"memory":true}'
END
WHERE json_valid(builtin) AND substr(trim(builtin), 1, 1) = '{' AND instr(builtin, '"memory"') = 0;

-- +goose Down
ALTER TABLE tbl_setting DROP COLUMN project_history_limit;
UPDATE tbl_agent
SET builtin = replace(replace(replace(builtin, ',"memory":true', ''), '"memory":true,', ''), '"memory":true', '')
WHERE json_valid(builtin) AND substr(trim(builtin), 1, 1) = '{';
