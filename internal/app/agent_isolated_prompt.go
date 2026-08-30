package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"codingto/internal/applog"
	"codingto/internal/piagent"
)

func (s *AgentService) generateIsolatedText(provider, model, workDir, prompt string) (string, error) {
	cfg := s.store.Get()
	if provider == "" || model == "" {
		provider, model = cfg.DefaultProvider, cfg.DefaultModel
	}
	if provider == "" || model == "" {
		return "", fmt.Errorf("no provider or model selected")
	}
	if err := piagent.ValidateProviders(cfg.Providers, provider, model); err != nil {
		return "", err
	}
	if err := validateProviderCredentials(cfg.Providers, provider); err != nil {
		return "", err
	}
	selectedModel, found := piagent.FindModel(cfg.Providers, provider, model)
	if !found {
		return "", fmt.Errorf("model not found: %s/%s", provider, model)
	}

	dataDir, err := os.MkdirTemp("", "codingto-isolated-")
	if err != nil {
		return "", fmt.Errorf("create isolated model directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(dataDir); removeErr != nil {
			applog.Errorf("remove isolated model directory: %v", removeErr)
		}
	}()
	if err := piagent.WriteModels(dataDir, cfg.Providers); err != nil {
		return "", fmt.Errorf("write isolated model configuration: %w", err)
	}

	adapter := piagent.NewAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := adapter.Start(ctx, piagent.StartConfig{
		WorkDir: workDir, AgentDir: dataDir, Provider: provider, Model: model,
		ExtraArgs: []string{"--no-builtin-tools"},
		Env:       map[string]string{"PI_CODING_AGENT_DIR": dataDir},
	}); err != nil {
		return "", err
	}
	defer func() {
		if stopErr := adapter.Stop(); stopErr != nil {
			applog.Errorf("stop isolated Git message model: %v", stopErr)
		}
	}()
	if err := adapter.SendCommand(mustJSON(map[string]string{"type": "set_model", "provider": provider, "modelId": model})); err != nil {
		return "", err
	}
	if selectedModel.Reasoning {
		if err := adapter.SendCommand(mustJSON(map[string]string{"type": "set_thinking_level", "level": "off"})); err != nil {
			return "", err
		}
	}
	if err := adapter.SendCommand(mustJSON(map[string]any{"type": "prompt", "message": prompt})); err != nil {
		return "", err
	}
	output, _, err := waitForText(ctx, adapter)
	if err != nil {
		return "", err
	}
	applog.Infof("isolated Git message generation completed: prompt_chars=%d output_chars=%d", len([]rune(prompt)), len([]rune(output)))
	return output, nil
}
