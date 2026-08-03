package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"codingto/internal/piagent"
	"codingto/internal/subagentbridge"
)

const subagentSnapshotFile = ".subagent-config.json"

func resolveSubagentBridgeBinary() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CODINGTO_SUBAGENT_BRIDGE_BIN")); configured != "" {
		return validateSubagentBridgeBinary(configured)
	}
	executableName := "subagent-bridge"
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	candidates := []string{}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), executableName))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "bin", executableName),
			filepath.Join(cwd, executableName),
		)
	}
	for _, candidate := range candidates {
		if path, err := validateSubagentBridgeBinary(candidate); err == nil {
			return path, nil
		}
	}
	// Fallback: the unified CodingTo executable can act as the bridge itself.
	if executable, err := os.Executable(); err == nil {
		if path, err := validateSubagentBridgeBinary(executable); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("Subagent Bridge helper 未安装；应与 CodingTo 一同构建并放在应用可执行文件旁")
}

func validateSubagentBridgeBinary(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Subagent Bridge helper 不是普通文件")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, absolute, "subagent-bridge", "version", "--json")
	configureDocumentBridgeProcess(command)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("Subagent Bridge helper 无法执行: %w", err)
	}
	var version struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := json.Unmarshal(output, &version); err != nil || version.ProtocolVersion != subagentbridge.ProtocolVersion {
		return "", fmt.Errorf("Subagent Bridge helper 协议不兼容")
	}
	return absolute, nil
}

func (s *AgentService) prepareSubagentRuntime(
	req PromptRequest,
	cfg AppConfig,
	parent AgentProfile,
	sessionDir string,
	agentEnv map[string]string,
) error {
	if !parent.Builtin["subagent"] {
		return nil
	}
	bridgeBinary, err := resolveSubagentBridgeBinary()
	if err != nil {
		return err
	}
	authorized := make(map[string]struct{}, len(parent.SubAgents))
	for _, key := range parent.SubAgents {
		authorized[key] = struct{}{}
	}
	agents := make([]subagentbridge.AgentConfig, 0, len(authorized))
	for _, child := range cfg.Agents {
		if _, allowed := authorized[child.ID]; !allowed || child.ID == parent.ID {
			continue
		}
		if err := piagent.WriteModels(child.DataDir, cfg.Providers); err != nil {
			return fmt.Errorf("write subagent %s models.json: %w", child.Name, err)
		}
		childEnv := agentProcessEnv(cfg, child)
		if selected, found := piagent.FindModel(cfg.Providers, child.DefaultProvider, child.DefaultModel); found {
			childEnv["CODINGTO_MODEL_INPUT_MODALITIES"] = strings.Join(selected.Input, ",")
			agents = append(agents, subagentbridge.AgentConfig{
				Key: child.ID, Name: child.Name, Description: child.Description,
				DataDir: child.DataDir, Provider: child.DefaultProvider, Model: child.DefaultModel,
				ThinkingLevel: selected.DefaultThinkingLevel, Input: append([]string(nil), selected.Input...),
				Builtin: cloneBoolMap(child.Builtin), PiTools: cloneBoolMap(child.PiTools), Env: childEnv,
			})
		} else {
			agents = append(agents, subagentbridge.AgentConfig{
				Key: child.ID, Name: child.Name, Description: child.Description,
				DataDir: child.DataDir, Provider: child.DefaultProvider, Model: child.DefaultModel,
				ConfigError: fmt.Sprintf("default model %s/%s is not available", child.DefaultProvider, child.DefaultModel),
				Builtin:     cloneBoolMap(child.Builtin), PiTools: cloneBoolMap(child.PiTools), Env: childEnv,
			})
		}
		index := len(agents) - 1
		if child.Builtin["document"] {
			documentBinary, err := resolveDocumentBridgeBinary()
			if err != nil {
				return fmt.Errorf("prepare document bridge for subagent %s: %w", child.Name, err)
			}
			agents[index].Env["CODINGTO_DOCUMENT_BRIDGE_BIN"] = documentBinary
		}
		if s.runtimeEnv != nil {
			for key, value := range s.runtimeEnv(child.ID, req.SessionID) {
				agents[index].Env[key] = value
			}
		}
	}
	snapshot := subagentbridge.Snapshot{
		Version:    subagentbridge.SnapshotVersion,
		SessionDir: filepath.Clean(sessionDir), WorkDir: filepath.Clean(req.WorkDir),
		MaxConcurrency: cfg.SubagentConcurrency,
		Agents:         agents,
	}
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	configPath := filepath.Join(sessionDir, subagentSnapshotFile)
	if err := os.WriteFile(configPath, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write subagent snapshot: %w", err)
	}
	if err := os.Chmod(configPath, 0o600); err != nil {
		return err
	}
	keys := make([]string, 0, len(agents))
	for _, agent := range agents {
		keys = append(keys, agent.Key)
	}
	agentEnv["CODINGTO_SUBAGENT_KEYS"] = strings.Join(keys, ",")
	agentEnv["CODINGTO_SUBAGENT_MAX_CONCURRENCY"] = fmt.Sprintf("%d", snapshot.MaxConcurrency)
	agentEnv["CODINGTO_SUBAGENT_BRIDGE_BIN"] = bridgeBinary
	agentEnv["CODINGTO_SUBAGENT_CONFIG"] = configPath
	return nil
}

func cloneBoolMap(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
