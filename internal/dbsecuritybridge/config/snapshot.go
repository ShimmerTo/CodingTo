package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"codingto/internal/dbsecurity"
)

// Snapshot 管理 0600 配置快照的读取与热更新：
// 每次 Config() 调用只做一次 stat，mtime 变化才重读文件，
// 无 watcher、无轮询。
type Snapshot struct {
	mu     sync.Mutex
	path   string
	config dbsecurity.DBConfig
	mtime  time.Time
	loaded bool
}

func NewSnapshot(path string) *Snapshot {
	return &Snapshot{path: path}
}

// Path 返回快照文件路径（审计目录由其 dirname 推导）。
func (s *Snapshot) Path() string {
	return s.path
}

// Config 返回当前快照配置；文件变化时自动重载。
// 快照不存在视为空配置（无任何可见连接）。
func (s *Snapshot) Config() (dbsecurity.DBConfig, error) {
	info, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.mu.Lock()
			s.config = dbsecurity.DBConfig{}
			s.loaded = true
			s.mtime = time.Time{}
			s.mu.Unlock()
			return dbsecurity.DBConfig{}, nil
		}
		return dbsecurity.DBConfig{}, fmt.Errorf("读取配置快照失败：%w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && info.ModTime().Equal(s.mtime) {
		return s.config, nil
	}

	raw, err := os.ReadFile(s.path)
	if err != nil {
		return dbsecurity.DBConfig{}, fmt.Errorf("读取配置快照失败：%w", err)
	}
	var config dbsecurity.DBConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return dbsecurity.DBConfig{}, fmt.Errorf("配置快照格式错误：%w", err)
	}
	config.Normalize()
	s.config = config
	s.mtime = info.ModTime()
	s.loaded = true
	return s.config, nil
}
