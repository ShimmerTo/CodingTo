package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"codingto/internal/sshsecurity"
)

// Snapshot reloads a private session config only when its mtime changes.
type Snapshot struct {
	mu     sync.Mutex
	path   string
	config sshsecurity.Config
	mtime  time.Time
	loaded bool
}

// NewSnapshot creates a lazy SSH snapshot loader.
func NewSnapshot(path string) *Snapshot { return &Snapshot{path: path} }

// Config returns the normalized current snapshot.
func (s *Snapshot) Config() (sshsecurity.Config, error) {
	info, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return sshsecurity.Config{}, nil
		}
		return sshsecurity.Config{}, fmt.Errorf("读取 SSH 配置快照失败：%w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && info.ModTime().Equal(s.mtime) {
		return s.config, nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return sshsecurity.Config{}, fmt.Errorf("读取 SSH 配置快照失败：%w", err)
	}
	var cfg sshsecurity.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return sshsecurity.Config{}, fmt.Errorf("SSH 配置快照格式错误：%w", err)
	}
	cfg.Normalize()
	s.config, s.mtime, s.loaded = cfg, info.ModTime(), true
	return cfg, nil
}
