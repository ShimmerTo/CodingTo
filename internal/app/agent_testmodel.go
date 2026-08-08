package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"codingto/internal/applog"
	"codingto/internal/piagent"
)

// TestModelRequest asks the backend to verify a single model responds through
// the Pi agent runtime without disturbing any running conversation.
type TestModelRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type TestModelResult struct {
	OK      bool   `json:"ok"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
	Latency int64  `json:"latencyMs,omitempty"`
}

// TestModel spins up an isolated Pi process, runs a trivial prompt, and reports
// whether the model answered. It shares no state with the interactive agent.
func (s *AgentService) TestModel(req TestModelRequest) (TestModelResult, error) {
	applog.Infof("[TestModel] start: provider=%q model=%q", req.Provider, req.Model)
	cfg := s.store.Get()
	if req.Provider == "" || req.Model == "" {
		req.Provider, req.Model = cfg.DefaultProvider, cfg.DefaultModel
		applog.Infof("[TestModel] empty input, fell back to default: provider=%q model=%q", req.Provider, req.Model)
	}
	if req.Provider == "" || req.Model == "" {
		applog.Infof("[TestModel] no provider or model selected")
		return TestModelResult{OK: false, Error: "no provider or model selected"}, nil
	}
	if err := piagent.ValidateProviders(cfg.Providers, req.Provider, req.Model); err != nil {
		applog.Infof("[TestModel] ValidateProviders failed: %v", err)
		return TestModelResult{OK: false, Error: err.Error()}, nil
	}
	if err := validateProviderCredentials(cfg.Providers, req.Provider); err != nil {
		applog.Infof("[TestModel] provider credentials are unavailable: %v", err)
		return TestModelResult{OK: false, Error: err.Error()}, nil
	}
	selectedModel, found := piagent.FindModel(cfg.Providers, req.Provider, req.Model)
	if !found {
		applog.Infof("[TestModel] model not found: %s/%s", req.Provider, req.Model)
		return TestModelResult{OK: false, Error: fmt.Sprintf("model not found: %s/%s", req.Provider, req.Model)}, nil
	}

	// Use a throwaway data dir so the test never touches a real agent session.
	testDir, err := os.MkdirTemp("", "codingto-modeltest-")
	if err != nil {
		return TestModelResult{OK: false, Error: fmt.Sprintf("create temp dir: %v", err)}, nil
	}
	defer os.RemoveAll(testDir)
	if err := piagent.WriteModels(testDir, cfg.Providers); err != nil {
		return TestModelResult{OK: false, Error: fmt.Sprintf("write models.json: %v", err)}, nil
	}
	applog.Infof("[TestModel] wrote models.json to %s, starting isolated Pi process", testDir)

	adapter := piagent.NewAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	start := time.Now()
	if err := adapter.Start(ctx, piagent.StartConfig{
		WorkDir:   testDir,
		Model:     req.Model,
		Provider:  req.Provider,
		ExtraArgs: []string{"--no-builtin-tools"},
		Env:       map[string]string{"PI_CODING_AGENT_DIR": testDir},
	}); err != nil {
		applog.Infof("[TestModel] adapter.Start failed after %dms: %v", time.Since(start).Milliseconds(), err)
		return TestModelResult{OK: false, Error: err.Error(), Latency: time.Since(start).Milliseconds()}, nil
	}
	defer adapter.Stop()
	applog.Infof("[TestModel] Pi process started after %dms, sending set_model", time.Since(start).Milliseconds())

	if err := adapter.SendCommand(mustJSON(map[string]string{"type": "set_model", "provider": req.Provider, "modelId": req.Model})); err != nil {
		applog.Infof("[TestModel] set_model failed: %v", err)
		return TestModelResult{OK: false, Error: err.Error(), Latency: time.Since(start).Milliseconds()}, nil
	}
	if selectedModel.Reasoning {
		if err := adapter.SendCommand(mustJSON(map[string]string{"type": "set_thinking_level", "level": "off"})); err != nil {
			applog.Infof("[TestModel] set_thinking_level failed: %v", err)
			return TestModelResult{OK: false, Error: err.Error(), Latency: time.Since(start).Milliseconds()}, nil
		}
	}
	prompt := map[string]any{"type": "prompt", "message": "Reply with the single word OK if you can read this."}
	applog.Infof("[TestModel] sending prompt: %q", prompt["message"])
	if err := adapter.SendCommand(mustJSON(prompt)); err != nil {
		applog.Infof("[TestModel] send prompt failed: %v", err)
		return TestModelResult{OK: false, Error: err.Error(), Latency: time.Since(start).Milliseconds()}, nil
	}
	applog.Infof("[TestModel] prompt sent, waiting for response (timeout 90s)")

	output, err := waitForText(ctx, adapter)
	if err != nil {
		applog.Infof("[TestModel] waitForText failed after %dms: %v", time.Since(start).Milliseconds(), err)
		return TestModelResult{OK: false, Error: err.Error(), Latency: time.Since(start).Milliseconds()}, nil
	}
	applog.Infof("[TestModel] success after %dms, output=%q", time.Since(start).Milliseconds(), output)
	return TestModelResult{OK: true, Output: output, Latency: time.Since(start).Milliseconds()}, nil
}

// waitForText blocks until the Pi process emits a text delta or the context
// expires, returning the trimmed accumulated text.
func waitForText(ctx context.Context, adapter *piagent.Adapter) (string, error) {
	var buf strings.Builder
	for {
		select {
		case <-ctx.Done():
			applog.Infof("[TestModel] waitForText: context done (timeout). buffered=%q", buf.String())
			return buf.String(), fmt.Errorf("model test timed out")
		case evt, ok := <-adapter.Events():
			if !ok {
				msg := buf.String()
				applog.Infof("[TestModel] waitForText: events channel closed. buffered=%q", msg)
				if msg == "" {
					if err := adapter.ExitError(); err != nil {
						return "", fmt.Errorf("pi exited: %v", err)
					}
					return "", fmt.Errorf("pi exited before producing output")
				}
				return msg, nil
			}
			var payload struct {
				Type                  string `json:"type"`
				Delta                 string `json:"delta"`
				Text                  string `json:"text"`
				Content               string `json:"content"`
				Command               string `json:"command"`
				Success               *bool  `json:"success"`
				Error                 string `json:"error"`
				AssistantMessageEvent struct {
					Type    string `json:"type"`
					Delta   string `json:"delta"`
					Content string `json:"content"`
					Text    string `json:"text"`
				} `json:"assistantMessageEvent"`
			}
			if err := json.Unmarshal(evt.Raw, &payload); err != nil {
				continue
			}
			// 统一的文本收集：部分模型把回答直接放在 message_start / response 的
			// content、text 字段里，而不以 text_delta 流式增量返回。
			collect := func(text string) {
				if text != "" {
					buf.WriteString(text)
					applog.Infof("[TestModel] waitForText: text collected, total len=%d", buf.Len())
				}
			}
			switch payload.Type {
			case "message_update":
				switch payload.AssistantMessageEvent.Type {
				case "text_delta":
					collect(payload.AssistantMessageEvent.Delta)
				case "text_end":
					if buf.Len() == 0 {
						collect(payload.AssistantMessageEvent.Content)
					}
				case "error":
					return buf.String(), errors.New("model stream failed")
				}
			case "text_delta":
				collect(payload.Delta)
			case "message_start", "message_end":
				// reasoning 模型或非流式回答可能在这些事件里直接携带文本。
				if payload.Text != "" {
					collect(payload.Text)
				} else if payload.Content != "" {
					collect(payload.Content)
				} else if payload.AssistantMessageEvent.Content != "" {
					collect(payload.AssistantMessageEvent.Content)
				} else if payload.AssistantMessageEvent.Text != "" {
					collect(payload.AssistantMessageEvent.Text)
				}
			case "agent_end":
				// 本轮对话已结束（模型已回答），不必再等 text_delta 或进程退出。
				applog.Infof("[TestModel] waitForText: agent_end received, buffered len=%d", buf.Len())
				if strings.TrimSpace(buf.String()) == "" {
					// 优先返回模型/provider 返回的真实错误（如 404、鉴权失败等）。
					if msg := agentEndErrorMessage(evt.Raw); msg != "" {
						return "", errors.New(msg)
					}
					applog.Infof("[TestModel] waitForText: last raw event: %s", string(evt.Raw))
					return "", errors.New("model completed without a text response")
				}
				return buf.String(), nil
			case "response":
				if payload.Success != nil && !*payload.Success {
					if payload.Error == "" {
						payload.Error = payload.Command + " failed"
					}
					return buf.String(), errors.New(payload.Error)
				}
				// 兼容极少数把最终结果放在 response 顶层 text/content 的实现。
				if payload.Text != "" {
					collect(payload.Text)
				} else if payload.Content != "" {
					collect(payload.Content)
				}
			default:
				applog.Infof("[TestModel] waitForText: event type=%q", payload.Type)
			}
		}
	}
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
