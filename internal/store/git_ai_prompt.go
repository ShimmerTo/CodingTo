package store

import "time"

// GetGitAIPrompt returns the custom prompt for one fixed Git AI operation.
func (s *Store) GetGitAIPrompt(promptType string) (string, bool, error) {
	row, err := s.db.QuickQuery("tbl_git_ai_prompt", "prompt_text", map[string]any{"prompt_type": promptType}).One()
	if err != nil {
		return "", false, err
	}
	if len(row) == 0 {
		return "", false, nil
	}
	return asString(row["prompt_text"]), true, nil
}

// SaveGitAIPrompt atomically inserts or replaces one fixed Git AI prompt.
func (s *Store) SaveGitAIPrompt(promptType, promptText string) error {
	_, err := s.db.ExecBySql(`INSERT INTO tbl_git_ai_prompt (prompt_type, prompt_text, update_time)
		VALUES (?, ?, ?)
		ON CONFLICT(prompt_type) DO UPDATE SET
		prompt_text=excluded.prompt_text, update_time=excluded.update_time`,
		promptType, promptText, time.Now().Unix()).Exec()
	return err
}

// DeleteGitAIPrompt removes one custom prompt so the built-in default becomes effective.
func (s *Store) DeleteGitAIPrompt(promptType string) error {
	_, err := s.db.QuickDelete("tbl_git_ai_prompt", map[string]any{"prompt_type": promptType}).Exec()
	return err
}
