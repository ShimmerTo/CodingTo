package piagent

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	APIAnthropicMessages  = "anthropic-messages"
	APIOpenAICompletions  = "openai-completions"
	APIOpenAIResponses    = "openai-responses"
	APIGoogleGenerativeAI = "google-generative-ai"
)

var SupportedAPIs = []string{
	APIOpenAICompletions,
	APIOpenAIResponses,
	APIAnthropicMessages,
	APIGoogleGenerativeAI,
}

type CostTier struct {
	InputTokensAbove int     `json:"inputTokensAbove"`
	Input            float64 `json:"input"`
	Output           float64 `json:"output"`
	CacheRead        float64 `json:"cacheRead"`
	CacheWrite       float64 `json:"cacheWrite"`
}

type Cost struct {
	Input      float64    `json:"input"`
	Output     float64    `json:"output"`
	CacheRead  float64    `json:"cacheRead"`
	CacheWrite float64    `json:"cacheWrite"`
	Tiers      []CostTier `json:"tiers,omitempty"`
}

type Capabilities struct {
	// ToolCall is CodingTo metadata. Pi currently has no per-model tool-call
	// capability field, so false starts Pi without tools.
	ToolCall *bool `json:"toolCall,omitempty"`
}

type Model struct {
	ID                   string             `json:"id"`
	Name                 string             `json:"name,omitempty"`
	API                  string             `json:"api,omitempty"`
	BaseURL              string             `json:"baseUrl,omitempty"`
	Reasoning            bool               `json:"reasoning,omitempty"`
	ThinkingLevelMap     map[string]*string `json:"thinkingLevelMap,omitempty"`
	DefaultThinkingLevel string             `json:"defaultThinkingLevel,omitempty"`
	Input                []string           `json:"input,omitempty"`
	ContextWindow        int                `json:"contextWindow,omitempty"`
	MaxTokens            int                `json:"maxTokens,omitempty"`
	Cost                 *Cost              `json:"cost,omitempty"`
	Capabilities         Capabilities       `json:"capabilities,omitempty"`
	Compat               map[string]any     `json:"compat,omitempty"`
}

type Provider struct {
	Name       string            `json:"name"`
	Label      string            `json:"label"`
	Vendor     string            `json:"vendor,omitempty"`
	Type       string            `json:"type,omitempty"` // Legacy field; migrated to model API.
	API        string            `json:"api"`            // Legacy provider-level protocol; migrated to models.
	BaseURL    string            `json:"baseUrl"`
	APIKey     string            `json:"apiKey"`
	OAuth      string            `json:"oauth,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	AuthHeader bool              `json:"authHeader,omitempty"`
	Enabled    bool              `json:"enabled"`
	Compat     map[string]any    `json:"compat,omitempty"`
	Models     []Model           `json:"models"`
}

func Bool(value bool) *bool { return &value }

func (m *Model) Normalize() {
	m.ID = strings.TrimSpace(m.ID)
	m.Name = strings.TrimSpace(m.Name)
	m.API = strings.TrimSpace(m.API)
	m.BaseURL = strings.TrimSpace(m.BaseURL)
	if m.ContextWindow <= 0 {
		m.ContextWindow = 128000
	}
	if m.MaxTokens <= 0 {
		m.MaxTokens = 16384
	}
	if len(m.Input) == 0 {
		m.Input = []string{"text"}
	}
	if !slices.Contains(m.Input, "text") {
		m.Input = append([]string{"text"}, m.Input...)
	}
	m.Input = uniqueAllowed(m.Input, []string{"text", "image"})
	if m.Capabilities.ToolCall == nil {
		m.Capabilities.ToolCall = Bool(true)
	}
	if !m.Reasoning {
		m.DefaultThinkingLevel = "off"
		m.ThinkingLevelMap = nil
	} else if !isThinkingLevel(m.DefaultThinkingLevel) {
		m.DefaultThinkingLevel = "medium"
	}
}

func (m Model) SupportsImages() bool {
	return slices.Contains(m.Input, "image")
}

func (m Model) SupportsTools() bool {
	return m.Capabilities.ToolCall == nil || *m.Capabilities.ToolCall
}

func (m Model) SupportsThinkingLevel(level string) bool {
	if !m.Reasoning {
		return level == "" || level == "off"
	}
	if !isThinkingLevel(level) {
		return false
	}
	if mapped, exists := m.ThinkingLevelMap[level]; exists && mapped == nil {
		return false
	}
	return true
}

func (p *Provider) Normalize() {
	p.Name = strings.TrimSpace(p.Name)
	p.Label = strings.TrimSpace(p.Label)
	p.Vendor = strings.ToLower(strings.TrimSpace(p.Vendor))
	p.Type = strings.ToLower(strings.TrimSpace(p.Type))
	p.API = strings.ToLower(strings.TrimSpace(p.API))
	p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	p.APIKey = strings.TrimSpace(p.APIKey)
	if p.Label == "" {
		p.Label = p.Name
	}
	if p.Vendor == "" {
		p.Vendor = p.Type
	}
	// API used to live on the provider. Keep reading that legacy value, but
	// migrate it to models so one provider can expose multiple protocols.
	legacyAPI := p.API
	if legacyAPI == "" && p.Type != "" {
		legacyAPI = apiType(p.Type)
	}
	for i := range p.Models {
		if strings.TrimSpace(p.Models[i].API) == "" && legacyAPI != "" {
			p.Models[i].API = legacyAPI
		}
		p.Models[i].Normalize()
	}
	p.API = ""
}

func ValidateProviders(providers []Provider, defaultProvider, defaultModel string) error {
	providerNames := map[string]struct{}{}
	defaultFound := false
	hasAnyModel := false
	for i := range providers {
		provider := providers[i]
		provider.Normalize()
		if provider.Name == "" {
			return fmt.Errorf("服务商 %d 的标识为空", i+1)
		}
		if _, exists := providerNames[provider.Name]; exists {
			return fmt.Errorf("服务商标识重复：%s", provider.Name)
		}
		providerNames[provider.Name] = struct{}{}
		if provider.Enabled && provider.BaseURL == "" {
			return fmt.Errorf("服务商 %s 需要填写基础域名", provider.Name)
		}
		if provider.BaseURL != "" {
			parsed, err := url.Parse(provider.BaseURL)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return fmt.Errorf("服务商 %s 的基础域名无效", provider.Name)
			}
		}
		modelIDs := map[string]struct{}{}
		for j := range provider.Models {
			model := provider.Models[j]
			model.Normalize()
			hasAnyModel = true
			if model.ID == "" {
				return fmt.Errorf("服务商 %s 第 %d 个模型的请求时名称为空", provider.Name, j+1)
			}
			if _, exists := modelIDs[model.ID]; exists {
				return fmt.Errorf("服务商 %s 存在重复的模型请求时名称 %s", provider.Name, model.ID)
			}
			modelIDs[model.ID] = struct{}{}
			if model.API == "" {
				return fmt.Errorf("模型 %s 需要选择 API 协议", model.ID)
			}
			if !slices.Contains(SupportedAPIs, model.API) {
				return fmt.Errorf("模型 %s 使用了不支持的 API 协议 %s", model.ID, model.API)
			}
			if strings.HasPrefix(model.BaseURL, "//") {
				return fmt.Errorf("模型 %s 的 Base URL 路径必须以单个 / 开头", model.ID)
			}
			if model.BaseURL != "" && !strings.HasPrefix(model.BaseURL, "/") {
				parsed, err := url.Parse(model.BaseURL)
				if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
					return fmt.Errorf("模型 %s 的 Base URL 必须是 /v1 形式的路径或完整 URL", model.ID)
				}
			}
			if provider.Name == defaultProvider && model.ID == defaultModel && provider.Enabled {
				defaultFound = true
			}
		}
	}
	// 允许完全不配置模型（例如用户想先新建服务商稍后添加模型）。
	if !hasAnyModel {
		return nil
	}
	if defaultProvider != "" && defaultModel != "" && !defaultFound {
		return fmt.Errorf("默认模型 %s/%s 不存在，或所属服务商已停用", defaultProvider, defaultModel)
	}
	return nil
}

func FindModel(providers []Provider, providerName, modelID string) (Model, bool) {
	for _, provider := range providers {
		if provider.Name != providerName || !provider.Enabled {
			continue
		}
		for _, model := range provider.Models {
			if model.ID == modelID {
				model.Normalize()
				return model, true
			}
		}
	}
	return Model{}, false
}

type modelsFile struct {
	Providers map[string]providerConfig `json:"providers"`
}

type providerConfig struct {
	BaseURL    string            `json:"baseUrl,omitempty"`
	APIKey     string            `json:"apiKey,omitempty"`
	API        string            `json:"api,omitempty"`
	OAuth      string            `json:"oauth,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	AuthHeader bool              `json:"authHeader,omitempty"`
	Compat     map[string]any    `json:"compat,omitempty"`
	Models     []modelConfig     `json:"models,omitempty"`
}

type modelConfig struct {
	ID               string             `json:"id"`
	Name             string             `json:"name,omitempty"`
	API              string             `json:"api,omitempty"`
	BaseURL          string             `json:"baseUrl,omitempty"`
	Reasoning        bool               `json:"reasoning,omitempty"`
	ThinkingLevelMap map[string]*string `json:"thinkingLevelMap,omitempty"`
	Input            []string           `json:"input,omitempty"`
	ContextWindow    int                `json:"contextWindow,omitempty"`
	MaxTokens        int                `json:"maxTokens,omitempty"`
	Cost             *Cost              `json:"cost,omitempty"`
	Compat           map[string]any     `json:"compat,omitempty"`
}

func WriteModels(configDir string, providers []Provider) error {
	file := modelsFile{Providers: map[string]providerConfig{}}
	for _, provider := range providers {
		provider.Normalize()
		if provider.Name == "" || !provider.Enabled {
			continue
		}
		models := make([]modelConfig, 0, len(provider.Models))
		for _, model := range provider.Models {
			model.Normalize()
			models = append(models, modelConfig{
				ID:               model.ID,
				Name:             model.Name,
				API:              model.API,
				BaseURL:          resolveModelBaseURL(provider.BaseURL, model.BaseURL),
				Reasoning:        model.Reasoning,
				ThinkingLevelMap: model.ThinkingLevelMap,
				Input:            model.Input,
				ContextWindow:    model.ContextWindow,
				MaxTokens:        model.MaxTokens,
				Cost:             model.Cost,
				Compat:           cloneMap(model.Compat),
			})
		}
		file.Providers[provider.Name] = providerConfig{
			BaseURL:    provider.BaseURL,
			APIKey:     provider.APIKey,
			OAuth:      provider.OAuth,
			Headers:    provider.Headers,
			AuthHeader: provider.AuthHeader,
			Compat:     cloneMap(provider.Compat),
			Models:     models,
		}
	}
	if len(file.Providers) == 0 {
		// 允许没有启用服务商（例如所有模型已删除），写入空的模型配置即可。
		file.Providers = map[string]providerConfig{}
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(configDir, 0o700); err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(configDir, "models.json"), data)
}

// SyncModelsToAgents copies the freshly written models.json from updatedDir to
// every other agent data directory so all agents share the same model
// configuration after a single-agent update (e.g. the in-session "set model"
// action). It is a no-op for the source directory and skips directories that
// do not exist.
func SyncModelsToAgents(updatedDir string, otherDirs []string) error {
	source := filepath.Join(updatedDir, "models.json")
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	for _, dir := range otherDirs {
		if filepath.Clean(dir) == filepath.Clean(updatedDir) {
			continue
		}
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
		if err := atomicWriteFile(filepath.Join(dir, "models.json"), data); err != nil {
			return err
		}
	}
	return nil
}

// modelsWriteMu serializes model-config writes so that two goroutines (e.g. a
// SaveConfig call and an in-session "set model" action) never stage and rename
// temp files over the same destination concurrently.
var modelsWriteMu sync.Mutex

// atomicWriteFile writes data to a uniquely named temp file next to target and
// then atomically replaces target with it. Using a unique temp name avoids two
// concurrent writers clobbering each other's staging file. The final replace is
// retried with a short backoff because on Windows the destination file can be
// briefly locked by another process (a running agent reading its config, a file
// watcher, or antivirus), which otherwise surfaces as "The process cannot access
// the file because it is being used by another process."
func atomicWriteFile(target string, data []byte) error {
	modelsWriteMu.Lock()
	defer modelsWriteMu.Unlock()
	dir := filepath.Dir(target)
	f, err := os.CreateTemp(dir, ".models-*.tmp")
	if err != nil {
		return err
	}
	tmpName := f.Name()
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmpName)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := commitRename(tmpName, target); err != nil {
		return err
	}
	keep = true
	return nil
}

// commitRename atomically replaces dst with src, retrying briefly to absorb
// transient locks. The actual replace primitive (tryReplace) is platform
// specific.
func commitRename(src, dst string) error {
	const maxAttempts = 12
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(20*attempt) * time.Millisecond)
		}
		if err := tryReplace(src, dst); err != nil {
			lastErr = err
			// If our staging file vanished (e.g. removed by a concurrent
			// writer), there is nothing left to rename.
			if _, statErr := os.Stat(src); statErr != nil {
				return err
			}
			continue
		}
		return nil
	}
	return lastErr
}

func resolveModelBaseURL(providerBaseURL, modelBaseURL string) string {
	modelBaseURL = strings.TrimSpace(modelBaseURL)
	if modelBaseURL == "" {
		return ""
	}
	if !strings.HasPrefix(modelBaseURL, "/") {
		return strings.TrimRight(modelBaseURL, "/")
	}
	return strings.TrimRight(strings.TrimRight(providerBaseURL, "/")+modelBaseURL, "/")
}

func apiType(providerType string) string {
	switch strings.ToLower(providerType) {
	case "anthropic", APIAnthropicMessages:
		return APIAnthropicMessages
	case APIOpenAIResponses:
		return APIOpenAIResponses
	case "google", APIGoogleGenerativeAI:
		return APIGoogleGenerativeAI
	default:
		return APIOpenAICompletions
	}
}

func uniqueAllowed(values, allowed []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if slices.Contains(allowed, value) && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func isThinkingLevel(level string) bool {
	return slices.Contains([]string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}, level)
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
