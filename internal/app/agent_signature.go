package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"codingto/internal/piagent"
)

func providerCatalogSignature(providers []piagent.Provider) string {
	data, err := json.Marshal(providers)
	if err != nil {
		return ""
	}
	return string(data)
}

func skillFileSignature(skillPath string) string {
	if strings.TrimSpace(skillPath) == "" {
		return ""
	}
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:])
}

func agentRuntimeSignature(profile AgentProfile, cfg AppConfig) string {
	authorized := make(map[string]struct{}, len(profile.SubAgents))
	for _, key := range profile.SubAgents {
		authorized[key] = struct{}{}
	}
	children := make(map[string]AgentProfile, len(authorized))
	for _, child := range cfg.Agents {
		if _, allowed := authorized[child.ID]; allowed && child.ID != profile.ID {
			children[child.ID] = child
		}
	}
	value := struct {
		Parent   AgentProfile            `json:"parent"`
		Children map[string]AgentProfile `json:"children"`
	}{
		Parent: profile, Children: children,
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func disabledPiTools(configured map[string]bool) []string {
	disabled := []string{}
	for _, key := range []string{"read", "bash", "edit", "write"} {
		if enabled, exists := configured[key]; exists && !enabled {
			disabled = append(disabled, key)
		}
	}
	return disabled
}
