package subagentbridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	ProtocolVersion = 1
	SnapshotVersion = 1
)

type Snapshot struct {
	Version    int           `json:"version"`
	SessionDir string        `json:"sessionDir"`
	WorkDir    string        `json:"workDir"`
	Agents     []AgentConfig `json:"agents"`
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

type RunRecord struct {
	Version      int       `json:"version"`
	RunID        string    `json:"runId"`
	AgentKey     string    `json:"agentKey"`
	AgentName    string    `json:"agentName"`
	ParentNodeID string    `json:"parentNodeId"`
	ToolCallID   string    `json:"toolCallId"`
	ChildNodeIDs []string  `json:"childNodeIds"`
	Status       string    `json:"status"`
	Task         string    `json:"task"`
	Text         string    `json:"text,omitempty"`
	Error        string    `json:"error,omitempty"`
	StartedAt    int64     `json:"startedAt"`
	EndedAt      int64     `json:"endedAt,omitempty"`
	Files        []RunFile `json:"files"`
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
