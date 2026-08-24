package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxAuditBytes = 5 * 1024 * 1024

// Event is a metadata-only SSH capability audit entry; parameter values are excluded.
type Event struct {
	Time       string `json:"time"`
	ResourceID string `json:"resourceId"`
	Capability string `json:"capability"`
	Decision   string `json:"decision"`
	Reason     string `json:"reason,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	ExitCode   int    `json:"exitCode,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Recorder appends bounded audit metadata under .ssh-security.
type Recorder struct {
	mu   sync.Mutex
	dir  string
	file *os.File
	size int64
}

// NewRecorder creates a private audit directory.
func NewRecorder(sessionDir string) (*Recorder, error) {
	dir := filepath.Join(sessionDir, ".ssh-security")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Recorder{dir: dir}, nil
}

// Record appends one entry without persisting command parameters or output.
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
	if r.file == nil && r.open() != nil {
		return
	}
	if r.size+int64(len(raw)) > maxAuditBytes {
		r.rotate()
	}
	if n, err := r.file.Write(raw); err == nil {
		r.size += int64(n)
	}
}

// Close closes the audit file.
func (r *Recorder) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
}

func (r *Recorder) open() error {
	file, err := os.OpenFile(filepath.Join(r.dir, "audit.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	r.file, r.size = file, info.Size()
	return nil
}

func (r *Recorder) rotate() {
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
	current := filepath.Join(r.dir, "audit.jsonl")
	_ = os.Remove(current + ".1")
	_ = os.Rename(current, current+".1")
	_ = r.open()
}
