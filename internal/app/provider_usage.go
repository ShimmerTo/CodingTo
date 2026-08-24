package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"codingto/internal/applog"
)

// UsageWindow is one normalized provider quota window.
type UsageWindow struct {
	Percent      float64 `json:"percent"`
	ResetSeconds int64   `json:"resetSeconds"`
}

// UnmarshalJSON accepts flexible field names: percent/usage_percent/usedPercent,
// resetSeconds/remainingSeconds, and resetsAt (ISO 8601 → computed delta).
func (w *UsageWindow) UnmarshalJSON(data []byte) error {
	raw := map[string]any{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for key, val := range raw {
		normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
		switch normalized {
		case "percent", "usagepercent", "usedpercent":
			if v, ok := val.(float64); ok {
				w.Percent = v
			}
		case "resetseconds", "remainingseconds":
			if v, ok := val.(float64); ok {
				w.ResetSeconds = int64(v)
			}
		case "resetsat":
			if v, ok := val.(string); ok {
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					secs := time.Until(t).Seconds()
					if secs > 0 {
						w.ResetSeconds = int64(secs)
					}
				}
			}
		}
	}
	return nil
}

// ProviderUsage carries normalized provider quota windows. OpenCode Go fills
// rolling (5h), weekly (7d), and monthly (30d); ChatGPT fills the weekly window.
type ProviderUsage struct {
	Rolling UsageWindow `json:"rolling"`
	Weekly  UsageWindow `json:"weekly"`
	Monthly UsageWindow `json:"monthly"`
}

// usageEnvelope matches the real API shape: {"usage":{"rolling":...,...}}
type usageEnvelope struct {
	Usage ProviderUsage `json:"usage"`
}

const deepSeekBalanceURL = "https://api.deepseek.com/user/balance"

// ProviderBalanceItem is one currency balance returned by a provider.
type ProviderBalanceItem struct {
	Currency     string  `json:"currency"`
	TotalBalance float64 `json:"totalBalance"`
}

// ProviderBalance is the non-secret balance summary displayed for a provider.
type ProviderBalance struct {
	Available bool                  `json:"available"`
	Balances  []ProviderBalanceItem `json:"balances"`
}

type deepSeekBalanceEnvelope struct {
	Available    bool `json:"is_available"`
	BalanceInfos []struct {
		Currency     string `json:"currency"`
		TotalBalance string `json:"total_balance"`
	} `json:"balance_infos"`
}

// GetProviderUsage fetches quota usage for an OpenCode Go provider
// (GET {baseUrl}/v1/usage with the provider's API key). Other providers are
// rejected so the frontend polling can never hit an arbitrary base URL.
func (a *App) GetProviderUsage(providerName string) (ProviderUsage, error) {
	cfg := a.store.Get()
	var baseURL, apiKey string
	for _, p := range cfg.Providers {
		if p.Name != providerName {
			continue
		}
		baseURL = p.BaseURL
		apiKey = p.APIKey
		break
	}
	if baseURL == "" && apiKey == "" {
		return ProviderUsage{}, fmt.Errorf("provider not found: %s", providerName)
	}
	if !strings.Contains(strings.ToLower(baseURL), "opencode.ai/zen/go") {
		return ProviderUsage{}, fmt.Errorf("provider %s does not expose the OpenCode Go usage API", providerName)
	}
	key := resolveAPIKey(apiKey)
	if key == "" {
		return ProviderUsage{}, fmt.Errorf("provider %s has no usable API key", providerName)
	}
	return fetchProviderUsage(baseURL, key)
}

// GetProviderBalance fetches the balance for a configured DeepSeek provider.
// The destination is always the fixed official endpoint, never the configured
// Base URL, so a malformed provider URL cannot redirect the API key elsewhere.
func (a *App) GetProviderBalance(providerName string) (ProviderBalance, error) {
	cfg := a.store.Get()
	found := false
	baseURL := ""
	apiKey := ""
	for _, provider := range cfg.Providers {
		if provider.Name != providerName {
			continue
		}
		found = true
		baseURL = provider.BaseURL
		apiKey = provider.APIKey
		break
	}
	if !found {
		return ProviderBalance{}, fmt.Errorf("provider not found: %s", providerName)
	}
	if !isDeepSeekBaseURL(baseURL) {
		return ProviderBalance{}, fmt.Errorf("provider %s does not use the DeepSeek API", providerName)
	}
	key := resolveAPIKey(apiKey)
	if key == "" {
		return ProviderBalance{}, fmt.Errorf("provider %s has no usable API key", providerName)
	}
	balance, err := fetchDeepSeekBalance(key)
	if err != nil {
		applog.Errorf("query DeepSeek balance for provider %s: %v", providerName, err)
		return ProviderBalance{}, errors.New("无法查询 DeepSeek 余额，请稍后重试")
	}
	return balance, nil
}

func isDeepSeekBaseURL(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Hostname(), "api.deepseek.com")
}

func fetchDeepSeekBalance(apiKey string) (ProviderBalance, error) {
	req, err := http.NewRequest(http.MethodGet, deepSeekBalanceURL, nil)
	if err != nil {
		return ProviderBalance{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ProviderBalance{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ProviderBalance{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		applog.Infof("[GetProviderBalance] DeepSeek returned %d", resp.StatusCode)
		return ProviderBalance{}, fmt.Errorf("balance request failed: %s", resp.Status)
	}
	var envelope deepSeekBalanceEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ProviderBalance{}, fmt.Errorf("decode DeepSeek balance response: %w", err)
	}
	result := ProviderBalance{Available: envelope.Available, Balances: make([]ProviderBalanceItem, 0, len(envelope.BalanceInfos))}
	for _, item := range envelope.BalanceInfos {
		total, err := strconv.ParseFloat(item.TotalBalance, 64)
		if err != nil {
			return ProviderBalance{}, fmt.Errorf("decode DeepSeek total balance: %w", err)
		}
		result.Balances = append(result.Balances, ProviderBalanceItem{
			Currency:     item.Currency,
			TotalBalance: total,
		})
	}
	return result, nil
}

// fetchProviderUsage calls the OpenCode Go usage endpoint and decodes the
// response. The API returns {"usage":{"rolling":...,"weekly":...,"monthly":...}}
// with percent and resetsAt (ISO 8601) fields.
func fetchProviderUsage(baseURL, apiKey string) (ProviderUsage, error) {
	base := strings.TrimRight(baseURL, "/")
	path := "/v1/usage"
	if strings.HasSuffix(base, "/v1") {
		path = "/usage"
	}
	url := base + path
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return ProviderUsage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ProviderUsage{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ProviderUsage{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		applog.Infof("[GetProviderUsage] %s returned %d: %s", url, resp.StatusCode, truncateRunes(string(body), 200))
		return ProviderUsage{}, fmt.Errorf("usage request failed: %s", resp.Status)
	}
	var env usageEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		applog.Infof("[GetProviderUsage] decode failed (raw body: %s)", truncateRunes(string(body), 300))
		return ProviderUsage{}, fmt.Errorf("decode usage response: %w", err)
	}
	usage := env.Usage
	applog.Infof("[GetProviderUsage] %s -> rolling=%.0f%%/%ds weekly=%.0f%%/%ds monthly=%.0f%%/%ds",
		url, usage.Rolling.Percent, usage.Rolling.ResetSeconds,
		usage.Weekly.Percent, usage.Weekly.ResetSeconds,
		usage.Monthly.Percent, usage.Monthly.ResetSeconds)
	return usage, nil
}

// resolveAPIKey expands a provider API key literal: "$NAME" / "${NAME}" reads
// the environment variable, anything else is returned verbatim.
func resolveAPIKey(apiKey string) string {
	key := strings.TrimSpace(apiKey)
	envName := ""
	if strings.HasPrefix(key, "${") && strings.HasSuffix(key, "}") {
		envName = strings.TrimSuffix(strings.TrimPrefix(key, "${"), "}")
	} else if strings.HasPrefix(key, "$") {
		envName = strings.TrimPrefix(key, "$")
	}
	if envName == "" {
		return key
	}
	for index, char := range envName {
		if !(char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || index > 0 && char >= '0' && char <= '9') {
			return key
		}
	}
	return strings.TrimSpace(os.Getenv(envName))
}
