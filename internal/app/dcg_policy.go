package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dcgDisabledMarkerFile is a per-conversation marker living inside the session
// directory. When present with content "1", the DCG bridge skips interception
// for the whole conversation (主 Agent 与子 Agent 均实时读取同一标记)。
//
// 该标记只表达「本次对话不启用命令拦截」，不修改智能体 recommended.dcg
// 配置，也不触发 Agent 进程重启。会话运行中写入即刻生效（DCG 扩展每次
// bash 调用前读取该文件）。
const dcgDisabledMarkerFile = "dcg_disabled"

func writeDcgDisabledMarker(sessionDir string, disabled bool) error {
	if sessionDir == "" {
		return nil
	}
	marker := filepath.Join(sessionDir, dcgDisabledMarkerFile)
	if disabled {
		return os.WriteFile(marker, []byte("1"), 0o600)
	}
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func dcgDisabledMarkerStatus(sessionDir string) bool {
	if sessionDir == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(sessionDir, dcgDisabledMarkerFile))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "1"
}

// SetSessionDcgDisabled toggles the conversation-scoped DCG interception
// marker for one conversation. It never touches the agent's recommended.dcg
// extension configuration.
func (s *AgentService) SetSessionDcgDisabled(sessionID int64, disabled bool) error {
	session, ok, err := s.store.Store().SessionByID(sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("conversation not found: %d", sessionID)
	}
	return writeDcgDisabledMarker(session.SessionDir, disabled)
}

// GetSessionDcgDisabled reports whether this conversation currently has DCG
// interception disabled at the conversation scope (independent of the agent's
// own recommended.dcg setting).
func (s *AgentService) GetSessionDcgDisabled(sessionID int64) (bool, error) {
	session, ok, err := s.store.Store().SessionByID(sessionID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("conversation not found: %d", sessionID)
	}
	return dcgDisabledMarkerStatus(session.SessionDir), nil
}
