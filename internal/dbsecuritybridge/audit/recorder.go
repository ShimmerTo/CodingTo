package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	auditFileName = "audit.jsonl"
	// maxAuditBytes 是单文件上限，超过后滚动为 .1 备份。
	maxAuditBytes = 5 * 1024 * 1024
)

// Recorder 追加写 sessionDir/.db-security/audit.jsonl。
// 仅 append，不做高频 fsync；写失败静默丢弃，不影响主流程。
type Recorder struct {
	mu   sync.Mutex
	dir  string
	file *os.File
	size int64
}

// NewRecorder 初始化审计目录（0700）。目录创建失败时返回错误。
func NewRecorder(sessionDir string) (*Recorder, error) {
	dir := filepath.Join(sessionDir, ".db-security")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Recorder{dir: dir}, nil
}

// Record 追加一条审计记录。
func (r *Recorder) Record(event Event) {
	if event.Time == "" {
		event.Time = time.Now().Format(time.RFC3339)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return
	}
	raw = append(raw, '\n')

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		if err := r.open(); err != nil {
			return
		}
	}
	if r.size+int64(len(raw)) > maxAuditBytes {
		r.rotate()
	}
	if n, err := r.file.Write(raw); err == nil {
		r.size += int64(n)
	}
}

// Close 关闭审计文件。
func (r *Recorder) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
}

func (r *Recorder) open() error {
	path := filepath.Join(r.dir, auditFileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	r.file = file
	r.size = info.Size()
	return nil
}

// rotate 把当前文件滚动为 .1 备份（覆盖旧备份），重新打开新文件。
func (r *Recorder) rotate() {
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
	current := filepath.Join(r.dir, auditFileName)
	backup := current + ".1"
	_ = os.Remove(backup)
	_ = os.Rename(current, backup)
	_ = r.open()
}
