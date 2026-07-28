package store

import (
	"encoding/json"

	"codingto/internal/piagent"
)

// ProviderRepository persists providers and their models as two independent
// tables. The provider/model Go structs are normalized first, then the nested
// fields (Cost / ThinkingLevelMap / Compat / Headers / Input / Capabilities)
// are stored as JSON columns so we keep the relationship without exploding the
// schema into dozens of near-empty tables.

func (s *Store) ReplaceProviders(providers []piagent.Provider) error {
	tx, err := s.db.GetTx()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.Exec("DELETE FROM tbl_model"); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM tbl_provider"); err != nil {
		return err
	}

	for order, provider := range providers {
		provider.Normalize()
		headers, _ := json.Marshal(orEmptyMap(provider.Headers))
		compat, _ := json.Marshal(orEmptyMapAny(provider.Compat))
		_, err := tx.Exec(
			`INSERT INTO tbl_provider (name, label, vendor, api, base_url, api_key, oauth, headers, auth_header, enabled, compat, sort_order)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			provider.Name, provider.Label, provider.Vendor, provider.API, provider.BaseURL,
			provider.APIKey, provider.OAuth, string(headers), boolToInt(provider.AuthHeader),
			boolToInt(provider.Enabled), string(compat), order,
		)
		if err != nil {
			return err
		}
		for mOrder, model := range provider.Models {
			model.Normalize()
			input, _ := json.Marshal(orEmptySlice(model.Input))
			thinking, _ := json.Marshal(orEmptyMapPtr(model.ThinkingLevelMap))
			cost, _ := json.Marshal(orEmptyPtr(model.Cost))
			capabilities, _ := json.Marshal(model.Capabilities)
			modelCompat, _ := json.Marshal(orEmptyMapAny(model.Compat))
			_, err := tx.Exec(
				`INSERT INTO tbl_model (id, provider_name, name, api, base_url, reasoning, thinking_level_map, default_thinking_level, input, context_window, max_tokens, cost, capabilities, compat, sort_order)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				model.ID, provider.Name, model.Name, model.API, model.BaseURL, boolToInt(model.Reasoning),
				string(thinking), model.DefaultThinkingLevel, string(input), model.ContextWindow,
				model.MaxTokens, string(cost), string(capabilities), string(modelCompat), mOrder,
			)
			if err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// ListProviders returns all providers with their models, ordered by sort_order.
func (s *Store) ListProviders() ([]piagent.Provider, error) {
	providerRows, err := s.db.QueryBySql("SELECT name, label, vendor, api, base_url, api_key, oauth, headers, auth_header, enabled, compat, sort_order FROM tbl_provider ORDER BY sort_order ASC").All()
	if err != nil {
		return nil, err
	}
	modelRows, err := s.db.QueryBySql("SELECT id, provider_name, name, api, base_url, reasoning, thinking_level_map, default_thinking_level, input, context_window, max_tokens, cost, capabilities, compat, sort_order FROM tbl_model ORDER BY provider_name ASC, sort_order ASC").All()
	if err != nil {
		return nil, err
	}

	modelsByProvider := map[string][]piagent.Model{}
	for _, r := range modelRows {
		providerName := asString(r["provider_name"])
		model, err := rowToModel(r)
		if err != nil {
			return nil, err
		}
		modelsByProvider[providerName] = append(modelsByProvider[providerName], model)
	}

	providers := make([]piagent.Provider, 0, len(providerRows))
	for _, r := range providerRows {
		provider, err := rowToProvider(r)
		if err != nil {
			return nil, err
		}
		provider.Models = modelsByProvider[provider.Name]
		if provider.Models == nil {
			provider.Models = []piagent.Model{}
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

func rowToProvider(r map[string]any) (piagent.Provider, error) {
	var headers map[string]string
	_ = json.Unmarshal([]byte(asString(r["headers"])), &headers)
	var compat map[string]any
	_ = json.Unmarshal([]byte(asString(r["compat"])), &compat)
	return piagent.Provider{
		Name:       asString(r["name"]),
		Label:      asString(r["label"]),
		Vendor:     asString(r["vendor"]),
		API:        asString(r["api"]),
		BaseURL:    asString(r["base_url"]),
		APIKey:     asString(r["api_key"]),
		OAuth:      asString(r["oauth"]),
		Headers:    headers,
		AuthHeader: asInt(r["auth_header"]) != 0,
		Enabled:    asInt(r["enabled"]) != 0,
		Compat:     compat,
	}, nil
}

func rowToModel(r map[string]any) (piagent.Model, error) {
	var input []string
	_ = json.Unmarshal([]byte(asString(r["input"])), &input)
	var thinking map[string]*string
	_ = json.Unmarshal([]byte(asString(r["thinking_level_map"])), &thinking)
	var cost piagent.Cost
	_ = json.Unmarshal([]byte(asString(r["cost"])), &cost)
	var caps piagent.Capabilities
	_ = json.Unmarshal([]byte(asString(r["capabilities"])), &caps)
	var compat map[string]any
	_ = json.Unmarshal([]byte(asString(r["compat"])), &compat)
	return piagent.Model{
		ID:                   asString(r["id"]),
		Name:                 asString(r["name"]),
		API:                  asString(r["api"]),
		BaseURL:              asString(r["base_url"]),
		Reasoning:            asInt(r["reasoning"]) != 0,
		ThinkingLevelMap:     thinking,
		DefaultThinkingLevel: asString(r["default_thinking_level"]),
		Input:                input,
		ContextWindow:        int(asInt(r["context_window"])),
		MaxTokens:            int(asInt(r["max_tokens"])),
		Cost:                 &cost,
		Capabilities:         caps,
		Compat:               compat,
	}, nil
}

func orEmptyMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func orEmptyMapAny(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func orEmptyMapPtr(m map[string]*string) map[string]*string {
	if m == nil {
		return map[string]*string{}
	}
	return m
}

func orEmptySlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orEmptyPtr[T any](v *T) *T {
	return v
}
