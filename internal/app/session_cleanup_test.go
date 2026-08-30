package app

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"codingto/internal/store"
)

// TestSessionCleanupRetention validates the startup auto-cleanup end to end at
// the store boundary: settings persist, and runSessionCleanup removes expired
// sessions (database rows plus their on-disk directories) while keeping fresh
// or running ones.
func TestSessionCleanupRetention(t *testing.T) {
	home := t.TempDir()
	cfgStore := &ConfigStore{}
	var err error
	cfgStore.st, err = store.Open(filepath.Join(home, ".codingto"))
	if err != nil {
		t.Fatal(err)
	}
	defer cfgStore.st.Close()
	st := cfgStore.st

	set, err := st.GetSetting()
	if err != nil {
		t.Fatal(err)
	}
	set.SessionCleanupEnabled = true
	set.SessionCleanupDays = 1
	set.SessionDir = filepath.Join(home, "sessions")
	if err := os.MkdirAll(set.SessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSetting(set); err != nil {
		t.Fatal(err)
	}

	// 配置读回校验（迁移 + 持久化）。
	got, err := st.GetSetting()
	if err != nil {
		t.Fatal(err)
	}
	if !got.SessionCleanupEnabled || got.SessionCleanupDays != 1 {
		t.Fatalf("setting not persisted: %+v", got)
	}
	if cfg := cfgStore.Get(); !cfg.SessionCleanupEnabled || cfg.SessionCleanupDays != 1 {
		t.Fatalf("config assemble mismatch: %+v", cfg)
	}

	// 构造三个会话：7 天前（过期，目录存在）、3 天前（过期，目录存在）、
	// 今天（保留）。另一个 running 状态的旧会话也必须跳过。
	oldID := mkSessionForCleanup(t, st, set, "old 7d", 7, false)
	midID := mkSessionForCleanup(t, st, set, "mid 3d", 3, false)
	newID := mkSessionForCleanup(t, st, set, "fresh today", 0, false)
	runningID := mkSessionForCleanup(t, st, set, "running stale", 20, true)

	app := &App{store: cfgStore, agent: &AgentService{}}
	result := app.runSessionCleanup()
	if result.Skipped {
		t.Fatal("cleanup unexpectedly skipped")
	}
	if result.Cleaned != 2 {
		t.Fatalf("expected 2 cleaned, got %d (failed=%d err=%s)", result.Cleaned, result.Failed, result.Error)
	}

	// DB：旧会话已删，新会话与 running 保留。
	for _, id := range []int64{oldID, midID} {
		if _, ok, _ := st.SessionByID(id); ok {
			t.Errorf("session %d should have been deleted", id)
		}
		if _, err := os.Stat(filepath.Join(set.SessionDir, "s"+strconv.FormatInt(id, 10))); !os.IsNotExist(err) {
			t.Errorf("session dir s%d should have been removed", id)
		}
	}
	if _, ok, _ := st.SessionByID(newID); !ok {
		t.Error("fresh session should be kept")
	}
	run, ok, _ := st.SessionByID(runningID)
	if !ok {
		t.Error("running session should be kept")
	} else if run.Status != "running" {
		t.Errorf("running session status changed: %s", run.Status)
	}
	if _, err := os.Stat(filepath.Join(set.SessionDir, "s"+strconv.FormatInt(newID, 10))); err != nil {
		t.Errorf("fresh session dir should remain: %v", err)
	}
}

// TestSessionCleanupDisabled verifies the cleanup pass is a no-op when the
// switch is off, and that Normalize clamps the retention window to 1..100.
func TestSessionCleanupDisabled(t *testing.T) {
	if _, err := os.Stat(filepath.Join(".")); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	cfgStore := &ConfigStore{}
	var err error
	cfgStore.st, err = store.Open(filepath.Join(home, ".codingto"))
	if err != nil {
		t.Fatal(err)
	}
	defer cfgStore.st.Close()
	st := cfgStore.st

	// 默认状态：关闭、60 天。
	set, _ := st.GetSetting()
	if set.SessionCleanupEnabled {
		t.Fatal("cleanup should default to disabled")
	}
	if set.SessionCleanupDays != 60 {
		t.Fatalf("expected default 60 days, got %d", set.SessionCleanupDays)
	}
	result := (&App{store: cfgStore, agent: &AgentService{}}).runSessionCleanup()
	if !result.Skipped {
		t.Fatalf("expected skipped when disabled, got %+v", result)
	}

	// 天数会被 Normalize 收束到 1..100。
	bad := DefaultConfig()
	bad.SessionCleanupEnabled = true
	bad.SessionCleanupDays = 500
	bad.Normalize()
	if bad.SessionCleanupDays != 100 {
		t.Fatalf("expected clamp to 100, got %d", bad.SessionCleanupDays)
	}
	bad.SessionCleanupDays = 0
	bad.Normalize()
	if bad.SessionCleanupDays != 60 {
		t.Fatalf("expected default 60, got %d", bad.SessionCleanupDays)
	}
}

// mkSessionForCleanup creates a session with a real s{id} directory and
// backdates its update_time so cleanup treats it as daysOld days stale.
func mkSessionForCleanup(t *testing.T, st *store.Store, set store.Setting, title string, daysOld int, running bool) int64 {
	t.Helper()
	item, err := st.CreateSession(store.Session{
		AgentID:       "default",
		EnvironmentID: "env",
		Title:         title,
		SessionDir:    filepath.Join(set.SessionDir, "temp"),
		Provider:      "openai",
		Model:         "gpt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if running {
		if err := st.UpdateSession(item.ID, map[string]any{"status": "running"}); err != nil {
			t.Fatal(err)
		}
	}
	// 会话目录 base 必须匹配 s{id}，模拟真实布局。先回写目录再 backdate：
	// UpdateSession 会重置 update_time 为当前时间。
	realDir := filepath.Join(set.SessionDir, "s"+strconv.FormatInt(item.ID, 10))
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "codingto_events.jsonl"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSession(item.ID, map[string]any{"session_dir": realDir}); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-time.Duration(daysOld) * 24 * time.Hour).UnixMilli()
	if err := st.SetSessionUpdateTime(item.ID, ts); err != nil {
		t.Fatalf("backdate session %d: %v", item.ID, err)
	}
	return item.ID
}

// TestSessionCleanupKeepsStewardSessions verifies that sessions owned by the
// steward — the resident conversation referenced by the steward profile and
// sessions recorded as bot tasks — survive auto-cleanup even when their last
// update is older than the retention window, while ordinary stale sessions
// are still removed.
func TestSessionCleanupKeepsStewardSessions(t *testing.T) {
	home := t.TempDir()
	cfgStore := &ConfigStore{}
	var err error
	cfgStore.st, err = store.Open(filepath.Join(home, ".codingto"))
	if err != nil {
		t.Fatal(err)
	}
	defer cfgStore.st.Close()
	st := cfgStore.st

	set, err := st.GetSetting()
	if err != nil {
		t.Fatal(err)
	}
	set.SessionCleanupEnabled = true
	set.SessionCleanupDays = 1
	set.SessionDir = filepath.Join(home, "sessions")
	if err := os.MkdirAll(set.SessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSetting(set); err != nil {
		t.Fatal(err)
	}

	// 管家常驻会话：写入 profile.ResidentSessionID。
	residentID := mkSessionForCleanup(t, st, set, "管家-测试", 30, false)
	if _, err := st.SaveStewardProfile(store.StewardProfile{
		AgentID:           "default",
		Name:              "测试",
		ResidentSessionID: residentID,
		Enabled:           true,
	}); err != nil {
		t.Fatal(err)
	}

	// 管家派发的 bot 任务会话。
	taskID := mkSessionForCleanup(t, st, set, "bot task", 30, false)
	if _, err := st.CreateBotTask(store.BotTask{SessionID: taskID, Status: "finished"}); err != nil {
		t.Fatal(err)
	}

	// 普通过期会话，应被清理。
	plainID := mkSessionForCleanup(t, st, set, "plain stale", 30, false)

	app := &App{store: cfgStore, agent: &AgentService{}}
	result := app.runSessionCleanup()
	if result.Skipped {
		t.Fatal("cleanup unexpectedly skipped")
	}
	if result.Cleaned != 1 {
		t.Fatalf("expected 1 cleaned, got %d (failed=%d err=%s)", result.Cleaned, result.Failed, result.Error)
	}
	for _, id := range []int64{residentID, taskID} {
		if _, ok, _ := st.SessionByID(id); !ok {
			t.Errorf("steward session %d should be kept", id)
		}
		if _, err := os.Stat(filepath.Join(set.SessionDir, "s"+strconv.FormatInt(id, 10))); err != nil {
			t.Errorf("steward session dir s%d should remain: %v", id, err)
		}
	}
	if _, ok, _ := st.SessionByID(plainID); ok {
		t.Error("plain stale session should have been deleted")
	}
	if _, ok, _ := st.BotTaskBySessionID(taskID); !ok {
		t.Error("bot task record should have been kept")
	}
}
