package subagentbridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// Version 3 includes both the version-2 current-agent data-directory security
	// contract and the configured per-session concurrency contract. Older helpers
	// would silently keep their compiled-in limit and make Settings disagree with
	// actual execution, so hosts must reject them.
	ProtocolVersion = 3
	// Snapshot version 2 carries MaxConcurrency for the bridge semaphore.
	SnapshotVersion = 2

	DefaultConcurrency = 2
	MaxConcurrency     = 4
)

type Snapshot struct {
	Version        int           `json:"version"`
	SessionDir     string        `json:"sessionDir"`
	WorkDir        string        `json:"workDir"`
	MaxConcurrency int           `json:"maxConcurrency"`
	Agents         []AgentConfig `json:"agents"`
}

func NormalizeConcurrency(value int) int {
	if value < 1 {
		return DefaultConcurrency
	}
	if value > MaxConcurrency {
		return MaxConcurrency
	}
	return value
}

type AgentConfig struct {
	Key           string            `json:"key"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	DataDir       string            `json:"dataDir"`
	Provider      string            `json:"provider"`
	Model         string            `json:"model"`
	ConfigError   string            `json:"configError,omitempty"`
	ThinkingLevel string            `json:"thinkingLevel,omitempty"`
	Input         []string          `json:"input,omitempty"`
	Builtin       map[string]bool   `json:"builtin"`
	PiTools       map[string]bool   `json:"piTools"`
	Env           map[string]string `json:"env"`
}

type Request struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Action  string          `json:"action"`
	Params  json.RawMessage `json:"params"`
}

type Response struct {
	Version int            `json:"version"`
	ID      string         `json:"id"`
	OK      bool           `json:"ok"`
	Result  any            `json:"result,omitempty"`
	Error   *ResponseError `json:"error,omitempty"`
}

type ResponseError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

type Notification struct {
	Version  int             `json:"version"`
	Type     string          `json:"type"`
	RunID    string          `json:"runId"`
	AgentKey string          `json:"agentKey"`
	Event    json.RawMessage `json:"event"`
}

type RunParams struct {
	Key          string `json:"key"`
	Task         string `json:"task"`
	RunID        string `json:"runId"`
	ParentNodeID string `json:"parentNodeId"`
	ToolCallID   string `json:"toolCallId"`
}

type RunFile struct {
	Path   string `json:"path"`
	Change string `json:"change"`
	Kind   string `json:"kind"`
	Bytes  int64  `json:"bytes"`
}

type RunResult struct {
	RunID        string    `json:"runId"`
	AgentKey     string    `json:"agentKey"`
	ParentNodeID string    `json:"parentNodeId"`
	Status       string    `json:"status"`
	Text         string    `json:"text,omitempty"`
	Files        []RunFile `json:"files"`
	Error        string    `json:"error,omitempty"`
	Transcript   string    `json:"transcript"`
}

// TokenUsageStats is the cumulative token consumption of a sub-agent run, so
// the parent conversation can attribute the run's model spend to itself.
type TokenUsageStats struct {
	Input      int64 `json:"input"`
	Cached     int64 `json:"cached"`
	CacheWrite int64 `json:"cacheWrite"`
	Output     int64 `json:"output"`
	Total      int64 `json:"total"`
}

// TokenUsageRequest is one completed model request inside a sub-agent run.
// Keeping these compact counters in run.json lets the parent model statistics
// expose the same request-level detail as a main conversation.
type TokenUsageRequest struct {
	RequestKey string `json:"requestKey"`
	Timestamp  int64  `json:"timestamp"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	API        string `json:"api,omitempty"`
	Input      int64  `json:"input"`
	Cached     int64  `json:"cached"`
	CacheWrite int64  `json:"cacheWrite"`
	Output     int64  `json:"output"`
	Total      int64  `json:"total"`
	StopReason string `json:"stopReason,omitempty"`
	Success    bool   `json:"success"`
}

type RunRecord struct {
	Version       int                 `json:"version"`
	RunID         string              `json:"runId"`
	AgentKey      string              `json:"agentKey"`
	AgentName     string              `json:"agentName"`
	ParentNodeID  string              `json:"parentNodeId"`
	ToolCallID    string              `json:"toolCallId"`
	ChildNodeIDs  []string            `json:"childNodeIds"`
	Status        string              `json:"status"`
	Task          string              `json:"task"`
	Text          string              `json:"text,omitempty"`
	Error         string              `json:"error,omitempty"`
	Provider      string              `json:"provider,omitempty"`
	Model         string              `json:"model,omitempty"`
	TokenStats    *TokenUsageStats    `json:"tokenStats,omitempty"`
	TokenRequests []TokenUsageRequest `json:"tokenRequests,omitempty"`
	StartedAt     int64               `json:"startedAt"`
	EndedAt       int64               `json:"endedAt,omitempty"`
	Files         []RunFile           `json:"files"`
}

// Day returns the local calendar day the run ended on (YYYY-MM-DD), used to
// attribute the run's token spend to a day. Empty when the run never settled.
func (r RunRecord) Day() string {
	if r.EndedAt <= 0 {
		return ""
	}
	return time.UnixMilli(r.EndedAt).Format("2006-01-02")
}

func LoadSnapshot(path string) (Snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var value Snapshot
	if err := json.Unmarshal(raw, &value); err != nil {
		return Snapshot{}, fmt.Errorf("decode subagent snapshot: %w", err)
	}
	if value.Version != SnapshotVersion {
		return Snapshot{}, fmt.Errorf("unsupported subagent snapshot version: %d", value.Version)
	}
	value.SessionDir = filepath.Clean(value.SessionDir)
	value.WorkDir = filepath.Clean(value.WorkDir)
	value.MaxConcurrency = NormalizeConcurrency(value.MaxConcurrency)
	return value, nil
}

func (s Snapshot) Agent(key string) (AgentConfig, bool) {
	for _, agent := range s.Agents {
		if agent.Key == key {
			return agent, true
		}
	}
	return AgentConfig{}, false
}

func ReadRunRecord(path string) (RunRecord, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return RunRecord{}, err
	}
	var record RunRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return RunRecord{}, err
	}
	return record, nil
}
