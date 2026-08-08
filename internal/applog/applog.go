// Package applog provides the CodingTo client's application-wide file logging.
//
// Logs are written to <home>/.codingto/logs/codingto/YYYY/MM/DD.log — the same
// daily-rotation layout the desktop app has always used. Previously the only
// logger created there (extensions.Manager) never wrote anything, and all real
// diagnostics went to stderr/stdout, which is invisible in the GUI client.
// Init redirects the standard library log package into the daily file (while
// keeping stderr for terminal runs), so every existing log.Printf call lands
// on disk. Callers that need levels can use Infof/Warnf/Errorf/Debugf.
package applog

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/w896736588/go-tool/gstool"
)

const businessName = "codingto"

// logKeepDays bounds how long daily log files are retained on disk. Older
// files are removed by gstool's periodic cleaner.
const logKeepDays = 30

var (
	mu     sync.RWMutex
	logger *gstool.GsSlog
)

// Init initializes the application-wide file logger and redirects the standard
// library log package into it. It is safe to call multiple times: subsequent
// calls return the existing logger. Logs keep going to stderr as well, so
// terminal runs behave exactly as before. Startup failures here are never fatal
// to the app: the client keeps running and logs to the console only.
func Init() error {
	mu.Lock()
	if logger != nil {
		mu.Unlock()
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		mu.Unlock()
		return fmt.Errorf("resolve user home directory: %w", err)
	}
	logDir := filepath.Join(home, ".codingto", "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		mu.Unlock()
		return fmt.Errorf("create log directory: %w", err)
	}
	_ = os.Chmod(logDir, 0o700)

	gs := gstool.NewSlog3(logDir, businessName)
	logger = gs
	// Send every standard-library log line (used across internal/app,
	// internal/piagent, internal/steward and internal/subagentbridge) into
	// the daily file as well, keeping the console output for terminal runs.
	// The file writer must come FIRST: io.MultiWriter stops at the first
	// writer that returns an error, and in a GUI launch (windowsgui, no
	// console) os.Stderr writes fail with an invalid-handle error. With
	// stderr first, every log.Printf record (steward inbound/outbound,
	// commands, RPC, SDK bridges, app errors) would be swallowed before it
	// ever reaches the daily file. stderr stays as a best-effort sink.
	// log.Logger issues exactly one Write per record, so the adapter sees
	// one line per call.
	log.SetOutput(io.MultiWriter(&stdWriter{gs}, os.Stderr))
	// Self-check: when the standard-log redirect works this line lands in the
	// daily file on every start, so the logging link is verifiable at a glance.
	log.Printf("applog: file logging ready: %s", logDir)
	mu.Unlock()

	// Retention is best-effort; a failure must not break startup. The log line
	// goes through gs directly (not the package helpers) to avoid re-locking.
	if err := gs.CleanOldLogs(logKeepDays); err != nil {
		gs.Infof("log retention setup failed: %v", err)
	}
	return nil
}

// Get returns the shared logger, or nil when Init has not been called yet.
func Get() *gstool.GsSlog {
	mu.RLock()
	defer mu.RUnlock()
	return logger
}

// Dir returns the directory that holds the daily log files, or "" when the
// user home directory cannot be resolved.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".codingto", "logs")
}

// stdWriter adapts the standard library log package to the file logger.
type stdWriter struct{ gs *gstool.GsSlog }

func (w *stdWriter) Write(p []byte) (int, error) {
	if line := strings.TrimRight(string(p), "\r\n"); line != "" {
		w.gs.Infof("%s", line)
	}
	return len(p), nil
}

// writeFallback 在 GsSlog 尚未初始化（例如子命令、applog.Init 之前或文件日志
// 初始化失败）时，把日志兜底写到 stderr，避免任何诊断日志被静默丢弃。
func writeFallback(level, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[codingto:%s] "+format+"\n", append([]any{level}, args...)...)
}

// Infof writes a formatted message at info level.
func Infof(format string, args ...any) {
	if gs := Get(); gs != nil {
		gs.Infof(format, args...)
		return
	}
	writeFallback("info", format, args...)
}

// Warnf writes a formatted message at warn level.
func Warnf(format string, args ...any) {
	if gs := Get(); gs != nil {
		gs.Warnf(format, args...)
		return
	}
	writeFallback("warn", format, args...)
}

// Errorf writes a formatted message at error level.
func Errorf(format string, args ...any) {
	if gs := Get(); gs != nil {
		gs.Errof(format, args...)
		return
	}
	writeFallback("error", format, args...)
}

// Debugf writes a formatted message at debug level.
func Debugf(format string, args ...any) {
	if gs := Get(); gs != nil {
		gs.Debugf(format, args...)
		return
	}
	writeFallback("debug", format, args...)
}

// Close syncs and closes the file logger. Safe to call multiple times; after
// Close the standard library log output falls back to stderr only.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if logger == nil {
		return
	}
	_ = logger.Close()
	logger = nil
	// Restore the standard library log output so any log.Printf after Close
	// goes to the console instead of writing into the just-closed file writer
	// (writing to a closed GsSlog is not guaranteed to be safe).
	log.SetOutput(os.Stderr)
}
