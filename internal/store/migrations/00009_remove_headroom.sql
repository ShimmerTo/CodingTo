-- +goose Up
-- +goose StatementBegin
UPDATE tbl_model
SET base_url =
    CASE api
        WHEN 'anthropic-messages' THEN json_extract((SELECT headroom FROM tbl_setting WHERE id = 1), '$.anthropicApiUrl')
        WHEN 'google-generative-ai' THEN json_extract((SELECT headroom FROM tbl_setting WHERE id = 1), '$.geminiApiUrl')
        ELSE json_extract((SELECT headroom FROM tbl_setting WHERE id = 1), '$.openaiApiUrl')
    END ||
    CASE
        WHEN base_url LIKE 'http://127.0.0.1:%/%' THEN substr(base_url, instr(substr(base_url, 8), '/') + 7)
        WHEN base_url LIKE 'http://localhost:%/%' THEN substr(base_url, instr(substr(base_url, 8), '/') + 7)
        ELSE ''
    END
WHERE (base_url LIKE 'http://127.0.0.1:%' OR base_url LIKE 'http://localhost:%')
  AND COALESCE(
      CASE api
          WHEN 'anthropic-messages' THEN json_extract((SELECT headroom FROM tbl_setting WHERE id = 1), '$.anthropicApiUrl')
          WHEN 'google-generative-ai' THEN json_extract((SELECT headroom FROM tbl_setting WHERE id = 1), '$.geminiApiUrl')
          ELSE json_extract((SELECT headroom FROM tbl_setting WHERE id = 1), '$.openaiApiUrl')
      END,
      ''
  ) <> '';

UPDATE tbl_provider
SET base_url = CASE
    WHEN EXISTS (
        SELECT 1 FROM tbl_model
        WHERE provider_name = tbl_provider.name AND api = 'anthropic-messages'
    ) THEN json_extract((SELECT headroom FROM tbl_setting WHERE id = 1), '$.anthropicApiUrl')
    WHEN EXISTS (
        SELECT 1 FROM tbl_model
        WHERE provider_name = tbl_provider.name AND api = 'google-generative-ai'
    ) THEN json_extract((SELECT headroom FROM tbl_setting WHERE id = 1), '$.geminiApiUrl')
    ELSE json_extract((SELECT headroom FROM tbl_setting WHERE id = 1), '$.openaiApiUrl')
END
WHERE (base_url LIKE 'http://127.0.0.1:%' OR base_url LIKE 'http://localhost:%')
  AND COALESCE(
      CASE
          WHEN EXISTS (
              SELECT 1 FROM tbl_model
              WHERE provider_name = tbl_provider.name AND api = 'anthropic-messages'
          ) THEN json_extract((SELECT headroom FROM tbl_setting WHERE id = 1), '$.anthropicApiUrl')
          WHEN EXISTS (
              SELECT 1 FROM tbl_model
              WHERE provider_name = tbl_provider.name AND api = 'google-generative-ai'
          ) THEN json_extract((SELECT headroom FROM tbl_setting WHERE id = 1), '$.geminiApiUrl')
          ELSE json_extract((SELECT headroom FROM tbl_setting WHERE id = 1), '$.openaiApiUrl')
      END,
      ''
  ) <> '';

ALTER TABLE tbl_setting DROP COLUMN headroom;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tbl_setting ADD COLUMN headroom TEXT NOT NULL DEFAULT '{}';
-- +goose StatementEnd
