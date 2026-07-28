package piagent

func ProviderPresets() []Provider {
	return []Provider{
		{
			Name: "openai", Label: "OpenAI", Vendor: "openai", API: APIOpenAIResponses,
			BaseURL: "https://api.openai.com/v1", APIKey: "$OPENAI_API_KEY", Enabled: true,
			Models: []Model{
				extendedReasoningModel("gpt-5.6-sol", "GPT-5.6 Sol", 272000, 128000, true),
				extendedReasoningModel("gpt-5.6-terra", "GPT-5.6 Terra", 272000, 128000, true),
				extendedReasoningModel("gpt-5.6-luna", "GPT-5.6 Luna", 272000, 128000, true),
			},
		},
		{
			Name: "anthropic", Label: "Anthropic", Vendor: "anthropic", API: APIAnthropicMessages,
			BaseURL: "https://api.anthropic.com", APIKey: "$ANTHROPIC_API_KEY", Enabled: true,
			Models: []Model{
				reasoningModel("claude-opus-4-8", "Claude Opus 4.8", 1000000, 128000, true),
				reasoningModel("claude-sonnet-5", "Claude Sonnet 5", 1000000, 128000, true),
				{ID: "claude-haiku-4-5-20251001", Name: "Claude Haiku 4.5", Input: []string{"text", "image"}, ContextWindow: 200000, MaxTokens: 64000, Capabilities: Capabilities{ToolCall: Bool(true)}},
			},
		},
		{
			Name: "google", Label: "Google Gemini", Vendor: "google", API: APIGoogleGenerativeAI,
			BaseURL: "https://generativelanguage.googleapis.com/v1beta", APIKey: "$GEMINI_API_KEY", Enabled: true,
			Models: []Model{
				reasoningModel("gemini-3.5-flash", "Gemini 3.5 Flash", 1000000, 65536, true),
				reasoningModel("gemini-3.1-pro-preview", "Gemini 3.1 Pro Preview", 1000000, 65536, true),
				reasoningModel("gemini-2.5-pro", "Gemini 2.5 Pro", 1000000, 65536, true),
			},
		},
		{
			Name: "deepseek", Label: "DeepSeek", Vendor: "deepseek", API: APIOpenAICompletions,
			BaseURL: "https://api.deepseek.com", APIKey: "$DEEPSEEK_API_KEY", Enabled: true,
			Models: []Model{
				{ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash", Reasoning: true, DefaultThinkingLevel: "high", Input: []string{"text"}, ContextWindow: 128000, MaxTokens: 64000, Capabilities: Capabilities{ToolCall: Bool(true)}},
				{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", Reasoning: true, DefaultThinkingLevel: "high", ThinkingLevelMap: highMaxThinkingMap(), Input: []string{"text"}, ContextWindow: 128000, MaxTokens: 64000, Capabilities: Capabilities{ToolCall: Bool(true)}},
			},
		},
		{
			Name: "openrouter", Label: "OpenRouter", Vendor: "openrouter", API: APIOpenAICompletions,
			BaseURL: "https://openrouter.ai/api/v1", APIKey: "$OPENROUTER_API_KEY", AuthHeader: true, Enabled: true,
			Compat: map[string]any{"thinkingFormat": "openrouter", "sessionAffinityFormat": "openrouter"},
			Models: []Model{
				extendedReasoningModel("openai/gpt-5.6", "GPT-5.6", 272000, 128000, true),
				reasoningModel("anthropic/claude-opus-4.8", "Claude Opus 4.8", 1000000, 128000, true),
				reasoningModel("google/gemini-3.5-flash", "Gemini 3.5 Flash", 1000000, 65536, true),
			},
		},
		{
			Name: "xai", Label: "xAI", Vendor: "xai", API: APIOpenAICompletions,
			BaseURL: "https://api.x.ai/v1", APIKey: "$XAI_API_KEY", Enabled: true,
			Models: []Model{
				reasoningModel("grok-4.5", "Grok 4.5", 256000, 65536, true),
				{ID: "grok-420-reasoning", Name: "Grok 420 Reasoning", Reasoning: true, DefaultThinkingLevel: "high", Input: []string{"text"}, ContextWindow: 256000, MaxTokens: 65536, Capabilities: Capabilities{ToolCall: Bool(true)}},
			},
		},
		{
			Name: "zai", Label: "Z.AI", Vendor: "zai", API: APIOpenAICompletions,
			BaseURL: "https://api.z.ai/api/paas/v4", APIKey: "$ZAI_API_KEY", Enabled: true,
			Compat: map[string]any{"thinkingFormat": "zai"},
			Models: []Model{
				{ID: "glm-5.1", Name: "GLM-5.1", Reasoning: true, DefaultThinkingLevel: "high", Input: []string{"text"}, ContextWindow: 200000, MaxTokens: 128000, Capabilities: Capabilities{ToolCall: Bool(true)}},
				reasoningModel("glm-5v-turbo", "GLM-5V Turbo", 200000, 128000, true),
			},
		},
		{
			Name: "ollama", Label: "Ollama", Vendor: "ollama", API: APIOpenAICompletions,
			BaseURL: "http://127.0.0.1:11434/v1", APIKey: "ollama", Enabled: true,
			Compat: map[string]any{"supportsDeveloperRole": false, "supportsReasoningEffort": false},
			Models: []Model{
				{ID: "qwen3-coder:30b", Name: "Qwen3 Coder 30B", Input: []string{"text"}, ContextWindow: 131072, MaxTokens: 32768, Capabilities: Capabilities{ToolCall: Bool(true)}},
				{ID: "gpt-oss:20b", Name: "GPT-OSS 20B", Reasoning: true, DefaultThinkingLevel: "medium", Input: []string{"text"}, ContextWindow: 131072, MaxTokens: 32768, Capabilities: Capabilities{ToolCall: Bool(true)}},
			},
		},
		{
			Name: "lmstudio", Label: "LM Studio", Vendor: "lmstudio", API: APIOpenAICompletions,
			BaseURL: "http://127.0.0.1:1234/v1", APIKey: "lm-studio", Enabled: true,
			Compat: map[string]any{"supportsDeveloperRole": false, "supportsReasoningEffort": false},
			Models: []Model{
				{ID: "local-model", Name: "Local Model", Input: []string{"text"}, ContextWindow: 128000, MaxTokens: 16384, Capabilities: Capabilities{ToolCall: Bool(true)}},
			},
		},
		{
			Name: "custom", Label: "OpenAI-compatible", Vendor: "custom", API: APIOpenAICompletions,
			BaseURL: "http://127.0.0.1:8080/v1", Enabled: true,
			Models: []Model{
				{ID: "model-id", Name: "Custom Model", Input: []string{"text"}, ContextWindow: 128000, MaxTokens: 16384, Capabilities: Capabilities{ToolCall: Bool(true)}},
			},
		},
	}
}

func DefaultProviders() []Provider {
	presets := ProviderPresets()
	return cloneProviders(presets[:4])
}

func reasoningModel(id, name string, contextWindow, maxTokens int, image bool) Model {
	input := []string{"text"}
	if image {
		input = append(input, "image")
	}
	return Model{
		ID: id, Name: name, Reasoning: true, DefaultThinkingLevel: "medium",
		Input: input, ContextWindow: contextWindow, MaxTokens: maxTokens,
		Capabilities: Capabilities{ToolCall: Bool(true)},
	}
}

func extendedReasoningModel(id, name string, contextWindow, maxTokens int, image bool) Model {
	model := reasoningModel(id, name, contextWindow, maxTokens, image)
	model.ThinkingLevelMap = extendedThinkingMap()
	return model
}

func extendedThinkingMap() map[string]*string {
	return map[string]*string{
		"xhigh": stringPtr("xhigh"),
		"max":   stringPtr("max"),
	}
}

func highMaxThinkingMap() map[string]*string {
	return map[string]*string{
		"minimal": nil,
		"low":     nil,
		"medium":  nil,
		"high":    stringPtr("high"),
		"xhigh":   nil,
		"max":     stringPtr("max"),
	}
}

func stringPtr(value string) *string { return &value }

func cloneProviders(providers []Provider) []Provider {
	result := make([]Provider, len(providers))
	for i, provider := range providers {
		result[i] = provider
		result[i].Headers = cloneStringMap(provider.Headers)
		result[i].Compat = cloneMap(provider.Compat)
		result[i].Models = make([]Model, len(provider.Models))
		for j, model := range provider.Models {
			result[i].Models[j] = model
			result[i].Models[j].Input = append([]string(nil), model.Input...)
			result[i].Models[j].Compat = cloneMap(model.Compat)
			if model.Capabilities.ToolCall != nil {
				result[i].Models[j].Capabilities.ToolCall = Bool(*model.Capabilities.ToolCall)
			}
			if model.ThinkingLevelMap != nil {
				result[i].Models[j].ThinkingLevelMap = make(map[string]*string, len(model.ThinkingLevelMap))
				for key, value := range model.ThinkingLevelMap {
					if value == nil {
						result[i].Models[j].ThinkingLevelMap[key] = nil
					} else {
						result[i].Models[j].ThinkingLevelMap[key] = stringPtr(*value)
					}
				}
			}
		}
	}
	return result
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
