package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const maxDCGConfigBytes = 1024 * 1024

// DCGConfigSource mirrors the source metadata emitted by `dcg config
// --format json`. Keeping the CLI as the source of truth avoids duplicating
// DCG's platform-specific path and precedence rules in CodingTo.
type DCGConfigSource struct {
	Level     string `json:"level"`
	Label     string `json:"label"`
	Path      string `json:"path"`
	Status    string `json:"status"`
	Authority string `json:"authority"`
	Detail    any    `json:"detail"`
}

// selectDCGUserConfigSource picks the writable user-level configuration file
// dcg actually loads (highest-precedence loaded source, else the last
// candidate), so CodingTo edits the same file dcg reads.
func selectDCGUserConfigSource(sources []DCGConfigSource) (string, bool, error) {
	var candidates []string
	var loaded []string
	seen := map[string]bool{}
	for _, source := range sources {
		if source.Level != "user" || source.Authority != "full" || strings.TrimSpace(source.Path) == "" {
			continue
		}
		path := filepath.Clean(source.Path)
		key := path
		if seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, path)
		if source.Status == "loaded" {
			loaded = append(loaded, path)
		}
	}
	if len(loaded) > 0 {
		return loaded[len(loaded)-1], len(loaded) > 1, nil
	}
	if len(candidates) > 0 {
		return candidates[len(candidates)-1], false, nil
	}
	return "", false, errors.New("dcg did not report a writable user configuration source")
}

func parseDCGConfig(content string) (map[string]any, error) {
	values := map[string]any{}
	if strings.TrimSpace(content) == "" {
		return values, nil
	}
	if err := toml.Unmarshal([]byte(content), &values); err != nil {
		return nil, err
	}
	return values, nil
}

func formatDCGConfig(values map[string]any) (string, error) {
	if values == nil {
		values = map[string]any{}
	}
	raw, err := toml.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("format dcg config: %w", err)
	}
	return string(raw), nil
}

func validateDCGConfig(binary, content string) error {
	temp, err := os.CreateTemp("", "codingto-dcg-*.toml")
	if err != nil {
		return fmt.Errorf("create dcg validation file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err = temp.WriteString(content); err != nil {
		temp.Close()
		return fmt.Errorf("write dcg validation file: %w", err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("close dcg validation file: %w", err)
	}
	if _, _, err = runDCGConfig(binary, tempPath); err != nil {
		return fmt.Errorf("dcg rejected the configuration: %w", err)
	}
	return nil
}

func runDCGConfig(binary, configPath string) (map[string]any, []DCGConfigSource, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "config", "--format", "json")
	configureDCGProcess(command)
	command.Env = append(os.Environ(), "DCG_NO_COLOR=1", "DCG_NO_UPDATE_CHECK=1")
	if configPath != "" {
		command.Env = append(command.Env, "DCG_CONFIG="+configPath)
	}
	raw, err := command.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, nil, errors.New("validation timed out")
	}
	if err != nil {
		message := strings.TrimSpace(string(raw))
		if message == "" {
			message = err.Error()
		}
		return nil, nil, errors.New(message)
	}
	result := map[string]any{}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, nil, fmt.Errorf("parse dcg validation output: %w", err)
	}
	metadata := struct {
		Sources []DCGConfigSource `json:"config_sources"`
	}{}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, nil, fmt.Errorf("parse dcg configuration sources: %w", err)
	}
	return result, metadata.Sources, nil
}

func dcgVersion(binary string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "--version")
	configureDCGProcess(command)
	command.Env = append(os.Environ(), "DCG_NO_COLOR=1", "DCG_NO_UPDATE_CHECK=1")
	raw, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
