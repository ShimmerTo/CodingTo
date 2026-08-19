package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"codingto/internal/applog"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// SessionCleanupResult reports one startup auto-cleanup pass. Skipped is true
// when cleanup is disabled by configuration. Cleaned/Failed count sessions.
type SessionCleanupResult struct {
	Skipped bool   `json:"skipped"`
	Enabled bool   `json:"enabled"`
	Days    int    `json:"days"`
	Cleaned int    `json:"cleaned"`
	Failed  int    `json:"failed"`
	Error   string `json:"error,omitempty"`
}

// startSessionCleanup launches the retention cleanup asynchronously at startup
// so it never delays window rendering. The result is kept for the frontend to
// fetch once and also broadcast through Wails events.
func (a *App) startSessionCleanup() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				applog.Errorf("session cleanup: panic: %v", r)
			}
		}()
		result := a.runSessionCleanup()
		a.cleanupMu.Lock()
		a.lastCleanup = result
		a.cleanupMu.Unlock()
		if application.Get() != nil && application.Get().Event != nil {
			application.Get().Event.Emit("session-cleanup:done", result)
		}
	}()
}

// runSessionCleanup deletes expired sessions: rows whose last update is older
// than the configured retention days, skipping conversations currently marked
// as running. Each deletion removes the database row (and its child records)
// plus the matching on-disk session directory under the shared session dir.
func (a *App) runSessionCleanup() *SessionCleanupResult {
	cfg := a.store.Get()
	result := &SessionCleanupResult{Enabled: cfg.SessionCleanupEnabled, Days: cfg.SessionCleanupDays}
	if !cfg.SessionCleanupEnabled {
		result.Skipped = true
		return result
	}
	cutoff := time.Now().Add(-time.Duration(cfg.SessionCleanupDays) * 24 * time.Hour).UnixMilli()
	sessions, err := a.store.Store().ListSessions()
	if err != nil {
		result.Error = fmt.Sprintf("list sessions: %v", err)
		applog.Errorf("session cleanup: %v", result.Error)
		return result
	}
	for _, item := range sessions {
		if item.Status == "running" || item.UpdateTime >= cutoff {
			continue
		}
		if err := a.deleteExpiredSession(item.ID, item.SessionDir); err != nil {
			result.Failed++
			applog.Errorf("session cleanup: delete session %d (%s): %v", item.ID, item.Title, err)
			continue
		}
		result.Cleaned++
	}
	applog.Infof("session cleanup: %d session(s) cleaned, %d failed (retention %d days)", result.Cleaned, result.Failed, cfg.SessionCleanupDays)
	return result
}

// deleteExpiredSession mirrors App.DeleteSession's safety contract: refuse to
// remove any directory that is not the expected s{id} folder, and stop the
// agent runtime before touching the filesystem or the database.
func (a *App) deleteExpiredSession(id int64, dir string) error {
	if err := a.agent.StopSession(id); err != nil {
		return err
	}
	if dir != "" {
		if filepath.Base(filepath.Clean(dir)) != fmt.Sprintf("s%d", id) {
			return fmt.Errorf("refuse to remove unexpected session directory: %s", dir)
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return a.store.Store().DeleteSession(id)
}

// GetSessionCleanupResult returns the most recent startup cleanup result and
// clears it, so the frontend shows the notice exactly once after launch.
func (a *App) GetSessionCleanupResult() *SessionCleanupResult {
	a.cleanupMu.Lock()
	defer a.cleanupMu.Unlock()
	result := a.lastCleanup
	a.lastCleanup = nil
	return result
}
